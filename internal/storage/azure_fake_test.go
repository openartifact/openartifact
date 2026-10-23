// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	fakeAzureAccount   = "acct"
	fakeAzureContainer = "cont"
)

// fakeAzureKey is the account key of the fake, base64 as Azure shows it (the
// encoding of "0123456789abcdef0123456789abcdef").
const fakeAzureKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// fakeAzureBlob is one blob of the fake.
type fakeAzureBlob struct {
	data            []byte
	etag            string
	modified        time.Time
	meta            map[string]string // lowercased metadata name -> value
	contentType     string
	contentEncoding string

	// copyStatus is "" for a blob written directly, else success, pending or failed.
	copyStatus  string
	copyPolls   int // HEADs left until a pending copy finishes
	copyFailure string
}

// fakeAzure is a minimal in-process model of the Blob service REST surface the
// store uses. It authenticates every request independently of the production
// signer: the Shared Key and SAS strings to sign are assembled here from the
// Microsoft documentation ("Authorize with Shared Key", "Create a service
// SAS"), so a wrong signature in the store fails the request with 403.
type fakeAzure struct {
	t         *testing.T
	account   string
	container string
	key       []byte
	prefix    string // URL path before the container: "" or "/devstoreaccount1"
	server    *httptest.Server

	clock atomic.Int64 // frozen time in unix nanos; 0 = the real clock

	mu       sync.Mutex
	seq      int
	blobs    map[string]*fakeAzureBlob
	staged   map[string]map[string][]byte // blob -> block id -> uncommitted block
	ops      map[string]int               // operations served, by name
	pageSize int                          // largest List Blobs page; 0 = what the client asks for

	// copyPolls makes a Copy Blob complete asynchronously after that many HEADs
	// of the destination; copyFails ends such a copy as failed.
	copyPolls int
	copyFails bool
	// onPendingHead runs for every HEAD that finds a copy still pending.
	onPendingHead func()
}

// fakeAzureOptions tunes newAzureFixture.
type fakeAzureOptions struct {
	account string // default fakeAzureAccount
	prefix  string // path-style account segment, e.g. "/devstoreaccount1"
	tls     bool
}

// newAzureFixture starts a fake Blob service and returns it with a store that
// talks to it through AzureEndpoint.
func newAzureFixture(t *testing.T, opts fakeAzureOptions) (*fakeAzure, *AzureBlobStore) {
	t.Helper()

	if opts.account == "" {
		opts.account = fakeAzureAccount
	}

	key, err := base64.StdEncoding.DecodeString(fakeAzureKey)
	if err != nil {
		t.Fatal(err)
	}

	f := &fakeAzure{
		t:         t,
		account:   opts.account,
		container: fakeAzureContainer,
		key:       key,
		prefix:    opts.prefix,
		blobs:     map[string]*fakeAzureBlob{},
		staged:    map[string]map[string][]byte{},
		ops:       map[string]int{},
	}

	if opts.tls {
		f.server = httptest.NewTLSServer(f)
	} else {
		f.server = httptest.NewServer(f)
	}

	t.Cleanup(f.server.Close)

	s, err := NewAzureBlobStore(StorageConfig{
		AzureAccountName:   opts.account,
		AzureAccountKey:    fakeAzureKey,
		AzureContainerName: fakeAzureContainer,
		AzureEndpoint:      f.server.URL + opts.prefix,
	})
	if err != nil {
		t.Fatal(err)
	}

	s.client = f.server.Client()
	s.now = f.now
	s.copyPollInterval = 0

	return f, s
}

// now is the fake's clock, shared with the store under test.
func (f *fakeAzure) now() time.Time {
	if v := f.clock.Load(); v != 0 {
		return time.Unix(0, v).UTC()
	}

	return time.Now().UTC()
}

// freeze stops the clock at t.
func (f *fakeAzure) freeze(t time.Time) { f.clock.Store(t.UnixNano()) }

// advance moves the (frozen) clock forward.
func (f *fakeAzure) advance(d time.Duration) { f.freeze(f.now().Add(d)) }

// blob returns a copy of the named blob, or nil.
func (f *fakeAzure) blob(name string) *fakeAzureBlob {
	f.mu.Lock()
	defer f.mu.Unlock()

	b := f.blobs[name]
	if b == nil {
		return nil
	}

	c := *b
	c.data = bytes.Clone(b.data)
	c.meta = maps.Clone(b.meta)

	return &c
}

// seed stores a blob directly; mod may adjust it.
func (f *fakeAzure) seed(name string, data []byte, mod func(*fakeAzureBlob)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	b := f.newBlobLocked(name, data)
	if mod != nil {
		mod(b)
	}
}

// opCount is how many requests of the named operation were served.
func (f *fakeAzure) opCount(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.ops[op]
}

// stagedBlocks is how many uncommitted blocks the blob has.
func (f *fakeAzure) stagedBlocks(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.staged[name])
}

func (f *fakeAzure) newBlobLocked(name string, data []byte) *fakeAzureBlob {
	f.seq++

	b := &fakeAzureBlob{
		data:        data,
		etag:        fmt.Sprintf(`"0x8DC%010X"`, f.seq),
		modified:    f.now().Truncate(time.Second),
		meta:        map[string]string{},
		contentType: "application/octet-stream",
	}
	f.blobs[name] = b

	return b
}

// fakeAzureOp is a classified request.
type fakeAzureOp struct {
	name string
	// sas lists the SAS permissions that allow the operation (any one of them);
	// empty means a SAS may not be used for it.
	sas string
}

// fakeAzureOps maps "<METHOD> <comp>" of a blob request to its operation.
func fakeAzureOps() map[string]fakeAzureOp {
	return map[string]fakeAzureOp{
		"PUT block":      {name: "put-block", sas: "w"},
		"PUT blocklist":  {name: "put-block-list", sas: "w"},
		"PUT properties": {name: "set-props", sas: "w"},
		"PUT metadata":   {name: "set-meta", sas: "w"},
		"PUT ":           {name: "put-blob", sas: "cw"},
		"GET blocklist":  {name: "get-block-list", sas: "r"},
		"GET ":           {name: "get-blob", sas: "r"},
		"HEAD ":          {name: "head", sas: "r"},
		"DELETE ":        {name: "delete", sas: "d"},
	}
}

func classifyAzureOp(r *http.Request, blob string) fakeAzureOp {
	q := r.URL.Query()

	if blob == "" && r.Method == http.MethodGet {
		// Container operations never take a SAS in these tests.
		if q.Get("comp") == "list" {
			return fakeAzureOp{name: "list"}
		}

		return fakeAzureOp{name: "container"}
	}

	if r.Method == http.MethodPut && q.Get("comp") == "" && r.Header.Get("X-Ms-Copy-Source") != "" {
		return fakeAzureOp{name: "copy", sas: "w"}
	}

	if op, ok := fakeAzureOps()[r.Method+" "+q.Get("comp")]; ok {
		return op
	}

	return fakeAzureOp{name: "unknown"}
}

func (f *fakeAzure) fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("X-Ms-Error-Code", code)
	w.WriteHeader(status)

	var escaped bytes.Buffer

	_ = xml.EscapeText(&escaped, []byte(msg))
	_, _ = fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"utf-8\"?><Error><Code>%s</Code><Message>%s</Message></Error>", code, escaped.String())
}

// blobName extracts the blob name from a path below <prefix>/<container>.
func (f *fakeAzure) blobName(p string) (string, bool) {
	base := f.prefix + "/" + f.container
	if p == base {
		return "", true
	}

	name, ok := strings.CutPrefix(p, base+"/")

	return name, ok && name != ""
}

func (f *fakeAzure) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	blob, ok := f.blobName(r.URL.Path)
	if !ok {
		f.fail(w, http.StatusNotFound, "ResourceNotFound", "no such path "+r.URL.Path)
		return
	}

	op := classifyAzureOp(r, blob)
	f.ops[op.name]++

	var err error
	if r.URL.Query().Has("sig") {
		err = f.checkSAS(r, blob, op)
	} else {
		err = f.checkSharedKey(r)
	}

	if err != nil {
		f.fail(w, http.StatusForbidden, "AuthenticationFailed", err.Error())
		return
	}

	switch op.name {
	case "list":
		f.list(w, r)
	case "container":
		w.WriteHeader(http.StatusOK)
	case "put-blob":
		f.putBlob(w, r, blob)
	case "put-block":
		f.putBlock(w, r, blob)
	case "put-block-list":
		f.putBlockList(w, r, blob)
	case "set-props":
		f.setProps(w, r, blob)
	case "set-meta":
		f.setMeta(w, r, blob)
	case "copy":
		f.copyBlob(w, r, blob)
	case "get-block-list":
		f.getBlockList(w, blob)
	case "get-blob", "head":
		f.getBlob(w, r, blob, op.name == "head")
	case "delete":
		f.deleteBlob(w, blob)
	default:
		f.fail(w, http.StatusBadRequest, "UnsupportedHttpVerb", r.Method+" "+r.URL.String())
	}
}

// checkSharedKey verifies the Authorization header against a string to sign
// assembled from the request exactly as the docs describe it.
func (f *fakeAzure) checkSharedKey(r *http.Request) error {
	auth := r.Header.Get("Authorization")

	sig, ok := strings.CutPrefix(auth, "SharedKey "+f.account+":")
	if !ok {
		return fmt.Errorf("no SharedKey authorization for account %s: %q", f.account, auth)
	}

	if v := r.Header.Get("X-Ms-Version"); v != azureAPIVersion {
		return fmt.Errorf("x-ms-version %q", v)
	}

	date, err := http.ParseTime(r.Header.Get("X-Ms-Date"))
	if err != nil {
		return fmt.Errorf("x-ms-date must be an RFC 1123 GMT date: %w", err)
	}

	if d := f.now().Sub(date); d > 15*time.Minute || d < -15*time.Minute {
		return fmt.Errorf("x-ms-date %s is too far from %s", date, f.now())
	}

	contentLength := ""
	if r.ContentLength > 0 {
		contentLength = strconv.FormatInt(r.ContentLength, 10)
	}

	var names []string

	for k := range r.Header {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-ms-") {
			names = append(names, lk)
		}
	}

	sort.Strings(names)

	var canonHeaders strings.Builder

	for _, n := range names {
		canonHeaders.WriteString(n + ":" + strings.TrimSpace(strings.Join(r.Header.Values(n), ",")) + "\n")
	}

	// "/<account>" + the request path as sent + sorted "name:value" query lines.
	var resource strings.Builder

	resource.WriteString("/" + f.account + r.URL.EscapedPath())

	params := map[string][]string{}

	for k, vs := range r.URL.Query() {
		params[strings.ToLower(k)] = append(params[strings.ToLower(k)], vs...)
	}

	var pnames []string
	for k := range params {
		pnames = append(pnames, k)
	}

	sort.Strings(pnames)

	for _, k := range pnames {
		vs := params[k]
		sort.Strings(vs)

		resource.WriteString("\n" + k + ":" + strings.Join(vs, ","))
	}

	stringToSign := r.Method + "\n" +
		r.Header.Get("Content-Encoding") + "\n" +
		r.Header.Get("Content-Language") + "\n" +
		contentLength + "\n" +
		r.Header.Get("Content-MD5") + "\n" +
		r.Header.Get("Content-Type") + "\n" +
		r.Header.Get("Date") + "\n" +
		r.Header.Get("If-Modified-Since") + "\n" +
		r.Header.Get("If-Match") + "\n" +
		r.Header.Get("If-None-Match") + "\n" +
		r.Header.Get("If-Unmodified-Since") + "\n" +
		r.Header.Get("Range") + "\n" +
		canonHeaders.String() + resource.String()

	if want := f.hmac(stringToSign); sig != want {
		return fmt.Errorf("shared key signature mismatch; the service computed it over %q", stringToSign)
	}

	return nil
}

func (f *fakeAzure) hmac(s string) string {
	mac := hmac.New(sha256.New, f.key)
	mac.Write([]byte(s))

	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// checkSAS verifies a service SAS (sv 2020-10-02) on a blob request.
func (f *fakeAzure) checkSAS(r *http.Request, blob string, op fakeAzureOp) error {
	q := r.URL.Query()

	if op.sas == "" || blob == "" {
		return fmt.Errorf("a SAS cannot be used for %s", op.name)
	}

	if q.Get("sv") != azureAPIVersion || q.Get("sr") != "b" {
		return fmt.Errorf("unsupported sv %q / sr %q", q.Get("sv"), q.Get("sr"))
	}

	sp := q.Get("sp")
	if !strings.ContainsAny(sp, op.sas) {
		return fmt.Errorf("sp=%q does not allow %s (needs one of %q)", sp, op.name, op.sas)
	}

	expiry, err := time.Parse("2006-01-02T15:04:05Z", q.Get("se"))
	if err != nil {
		return fmt.Errorf("se: %w", err)
	}

	if !f.now().Before(expiry) {
		return fmt.Errorf("the SAS expired at %s", expiry)
	}

	if q.Get("spr") == "https" && r.TLS == nil {
		return errors.New("the SAS only accepts https")
	}

	// Documented order for sv >= 2018-11-09 and < 2020-12-06.
	stringToSign := sp + "\n" +
		q.Get("st") + "\n" +
		q.Get("se") + "\n" +
		"/blob/" + f.account + "/" + f.container + "/" + blob + "\n" +
		q.Get("si") + "\n" +
		q.Get("sip") + "\n" +
		q.Get("spr") + "\n" +
		q.Get("sv") + "\n" +
		q.Get("sr") + "\n" +
		"\n" + // signed snapshot time
		q.Get("rscc") + "\n" +
		q.Get("rscd") + "\n" +
		q.Get("rsce") + "\n" +
		q.Get("rscl") + "\n" +
		q.Get("rsct")

	if want := f.hmac(stringToSign); q.Get("sig") != want {
		return fmt.Errorf("SAS signature mismatch; the service computed it over %q", stringToSign)
	}

	return nil
}

func metaOf(h http.Header) map[string]string {
	meta := map[string]string{}

	for k, vs := range h {
		if name, ok := strings.CutPrefix(strings.ToLower(k), "x-ms-meta-"); ok && len(vs) > 0 {
			meta[name] = vs[0]
		}
	}

	return meta
}

func (f *fakeAzure) putBlob(w http.ResponseWriter, r *http.Request, name string) {
	if r.Header.Get("X-Ms-Blob-Type") != "BlockBlob" {
		f.fail(w, http.StatusBadRequest, "MissingRequiredHeader", "x-ms-blob-type")
		return
	}

	body, _ := io.ReadAll(r.Body)
	b := f.newBlobLocked(name, body)
	b.meta = metaOf(r.Header)
	b.contentEncoding = r.Header.Get("X-Ms-Blob-Content-Encoding")

	if ct := r.Header.Get("X-Ms-Blob-Content-Type"); ct != "" {
		b.contentType = ct
	} else if ct := r.Header.Get("Content-Type"); ct != "" {
		b.contentType = ct
	}

	w.Header().Set("ETag", b.etag)
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeAzure) putBlock(w http.ResponseWriter, r *http.Request, name string) {
	id := r.URL.Query().Get("blockid")
	if raw, err := base64.StdEncoding.DecodeString(id); err != nil || len(raw) == 0 {
		f.fail(w, http.StatusBadRequest, "InvalidQueryParameterValue", "blockid must be base64")
		return
	}

	body, _ := io.ReadAll(r.Body)
	if len(body) == 0 {
		f.fail(w, http.StatusBadRequest, "InvalidHeaderValue", "a block cannot be empty")
		return
	}

	for other := range f.staged[name] {
		if len(other) != len(id) {
			f.fail(w, http.StatusBadRequest, "InvalidBlobOrBlock", "block ids of a blob must have the same length")
			return
		}
	}

	if f.staged[name] == nil {
		f.staged[name] = map[string][]byte{}
	}

	f.staged[name][id] = body

	w.WriteHeader(http.StatusCreated)
}

func (f *fakeAzure) putBlockList(w http.ResponseWriter, r *http.Request, name string) {
	dec := xml.NewDecoder(r.Body)

	var data []byte

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}

		el, ok := tok.(xml.StartElement)
		if !ok || (el.Name.Local != "Latest" && el.Name.Local != "Uncommitted") {
			continue
		}

		var id string
		if err := dec.DecodeElement(&id, &el); err != nil {
			f.fail(w, http.StatusBadRequest, "InvalidXmlDocument", err.Error())
			return
		}

		block, ok := f.staged[name][id]
		if !ok {
			f.fail(w, http.StatusBadRequest, "InvalidBlockList", "block "+id+" was not staged")
			return
		}

		data = append(data, block...)
	}

	b := f.newBlobLocked(name, data)
	b.contentType = r.Header.Get("X-Ms-Blob-Content-Type")

	if b.contentType == "" {
		b.contentType = "application/octet-stream"
	}

	// Committed blocks are no longer uncommitted; blocks left out are dropped.
	delete(f.staged, name)

	w.Header().Set("ETag", b.etag)
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeAzure) setProps(w http.ResponseWriter, r *http.Request, name string) {
	b := f.blobs[name]
	if b == nil {
		f.fail(w, http.StatusNotFound, "BlobNotFound", name)
		return
	}

	// Properties not given are cleared.
	b.contentType = r.Header.Get("X-Ms-Blob-Content-Type")
	b.contentEncoding = r.Header.Get("X-Ms-Blob-Content-Encoding")

	f.seq++
	b.etag = fmt.Sprintf(`"0x8DC%010X"`, f.seq)

	w.WriteHeader(http.StatusOK)
}

func (f *fakeAzure) setMeta(w http.ResponseWriter, r *http.Request, name string) {
	b := f.blobs[name]
	if b == nil {
		f.fail(w, http.StatusNotFound, "BlobNotFound", name)
		return
	}

	b.meta = metaOf(r.Header)

	f.seq++
	b.etag = fmt.Sprintf(`"0x8DC%010X"`, f.seq)

	w.WriteHeader(http.StatusOK)
}

func (f *fakeAzure) copyBlob(w http.ResponseWriter, r *http.Request, dst string) {
	source, err := url.Parse(r.Header.Get("X-Ms-Copy-Source"))
	if err != nil || source.Host != r.Host {
		f.fail(w, http.StatusBadRequest, "InvalidHeaderValue", "x-ms-copy-source "+r.Header.Get("X-Ms-Copy-Source"))
		return
	}

	srcName, ok := f.blobName(source.Path)

	src := f.blobs[srcName]
	if !ok || src == nil {
		f.fail(w, http.StatusNotFound, "CannotVerifyCopySource", source.Path)
		return
	}

	if cond := r.Header.Get("X-Ms-Source-If-Match"); cond != "" && cond != src.etag {
		f.fail(w, http.StatusPreconditionFailed, "SourceConditionNotMet", "source etag is "+src.etag)
		return
	}

	if old := f.blobs[dst]; old != nil && old.copyStatus == "pending" {
		f.fail(w, http.StatusConflict, "PendingCopyOperation", dst)
		return
	}

	b := f.newBlobLocked(dst, bytes.Clone(src.data))
	b.contentType, b.contentEncoding = src.contentType, src.contentEncoding

	// Metadata given in the request replaces the source's, otherwise it is inherited.
	if b.meta = metaOf(r.Header); len(b.meta) == 0 {
		b.meta = maps.Clone(src.meta)
	}

	b.copyStatus = "success"
	if f.copyPolls > 0 {
		b.copyStatus, b.copyPolls = "pending", f.copyPolls
	}

	w.Header().Set("ETag", b.etag)
	w.Header().Set("X-Ms-Copy-Id", "copy-"+strconv.Itoa(f.seq))
	w.Header().Set("X-Ms-Copy-Status", b.copyStatus)
	w.WriteHeader(http.StatusAccepted)
}

func (f *fakeAzure) getBlockList(w http.ResponseWriter, name string) {
	if f.blobs[name] == nil && len(f.staged[name]) == 0 {
		f.fail(w, http.StatusNotFound, "BlobNotFound", name)
		return
	}

	ids := make([]string, 0, len(f.staged[name]))
	for id := range f.staged[name] {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	var sb strings.Builder

	sb.WriteString(`<?xml version="1.0" encoding="utf-8"?><BlockList><UncommittedBlocks>`)

	for _, id := range ids {
		sb.WriteString("<Block><Name>" + id + "</Name><Size>" + strconv.Itoa(len(f.staged[name][id])) + "</Size></Block>")
	}

	sb.WriteString(`</UncommittedBlocks></BlockList>`)

	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, sb.String())
}

func (f *fakeAzure) getBlob(w http.ResponseWriter, r *http.Request, name string, head bool) {
	b := f.blobs[name]
	if b == nil {
		f.fail(w, http.StatusNotFound, "BlobNotFound", name)
		return
	}

	if b.copyStatus == "pending" {
		if f.onPendingHead != nil {
			f.onPendingHead()
		}

		if b.copyPolls--; b.copyPolls <= 0 {
			b.copyStatus = "success"
			if f.copyFails {
				b.copyStatus, b.copyFailure = "failed", "the copy failed"
			}
		}
	}

	if cond := r.Header.Get("If-Match"); cond != "" && cond != "*" && cond != b.etag {
		f.fail(w, http.StatusPreconditionFailed, "ConditionNotMet", "etag is "+b.etag)
		return
	}

	h := w.Header()
	h.Set("ETag", b.etag)
	h.Set("Last-Modified", b.modified.UTC().Format(http.TimeFormat))
	h.Set("Content-Type", b.contentType)

	if b.contentEncoding != "" {
		h.Set("Content-Encoding", b.contentEncoding)
	}

	// Response header overrides of a SAS.
	if v := r.URL.Query().Get("rsct"); v != "" {
		h.Set("Content-Type", v)
	}

	if v := r.URL.Query().Get("rscd"); v != "" {
		h.Set("Content-Disposition", v)
	}

	for k, v := range b.meta {
		h.Set("x-ms-meta-"+k, v)
	}

	if b.copyStatus != "" {
		h.Set("X-Ms-Copy-Status", b.copyStatus)
		h.Set("X-Ms-Copy-Status-Description", b.copyFailure)
	}

	h.Set("Content-Length", strconv.Itoa(len(b.data)))
	w.WriteHeader(http.StatusOK)

	if !head {
		_, _ = w.Write(b.data)
	}
}

func (f *fakeAzure) deleteBlob(w http.ResponseWriter, name string) {
	// A blob with only uncommitted blocks does not exist for Delete Blob; the
	// blocks stay until Azure expires them.
	if f.blobs[name] == nil {
		f.fail(w, http.StatusNotFound, "BlobNotFound", name)
		return
	}

	delete(f.blobs, name)
	delete(f.staged, name)

	w.WriteHeader(http.StatusAccepted)
}

type fakeAzureList struct {
	XMLName xml.Name          `xml:"EnumerationResults"`
	Blobs   []fakeAzureListed `xml:"Blobs>Blob"`
	Marker  string            `xml:"NextMarker"`
}

type fakeAzureListed struct {
	Name       string `xml:"Name"`
	Properties struct {
		LastModified  string `xml:"Last-Modified"`
		ContentLength int    `xml:"Content-Length"`
		ETag          string `xml:"Etag"`
	} `xml:"Properties"`
}

func (f *fakeAzure) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix, marker := q.Get("prefix"), q.Get("marker")

	limit, err := strconv.Atoi(q.Get("maxresults"))
	if err != nil || limit <= 0 {
		limit = 5000
	}

	if f.pageSize > 0 {
		limit = min(limit, f.pageSize)
	}

	names := make([]string, 0, len(f.blobs))

	for name := range f.blobs {
		if strings.HasPrefix(name, prefix) && name >= marker {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	var res fakeAzureList

	if len(names) > limit {
		res.Marker, names = names[limit], names[:limit]
	}

	for _, name := range names {
		b := f.blobs[name]

		var e fakeAzureListed

		e.Name = name
		e.Properties.LastModified = b.modified.UTC().Format(http.TimeFormat)
		e.Properties.ContentLength = len(b.data)
		e.Properties.ETag = b.etag
		res.Blobs = append(res.Blobs, e)
	}

	out, err := xml.Marshal(res)
	if err != nil {
		f.fail(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write(out)
}
