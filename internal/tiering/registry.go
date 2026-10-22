// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// DefaultPrimaryTierName names the primary tier until the database row says otherwise.
const DefaultPrimaryTierName = "hot"

var (
	// ErrTierUnavailable is returned for a tier whose backend could not be opened.
	ErrTierUnavailable = errors.New("storage tier unavailable")
	// ErrUnknownTier is returned for a tier or backend id the registry does not know.
	ErrUnknownTier = errors.New("unknown storage tier")

	errBackendConfig = errors.New("invalid storage backend config")
)

// Tier is one placement target with its opened blob store.
type Tier struct {
	ID            string
	Name          string
	BackendID     string // "" = the primary store
	BackendName   string
	Kind          string
	Rank          int
	PromoteOnRead bool
	// Store is nil while Err is set.
	Store storage.BlobStore
	// Err is why the tier's backend could not be opened ("" when it opened).
	Err error

	// identity is the canonical location of the tier's objects (see location),
	// "" when unknown. Two tiers with the same identity share their objects.
	identity string
}

// Primary reports whether the tier is the primary (hot) store.
func (t *Tier) Primary() bool { return t.BackendID == "" }

// Snapshot is an immutable view of the configured tiers.
type Snapshot struct {
	Tiers      []*Tier // hottest rank first
	primary    *Tier
	byID       map[string]*Tier
	byBackend  map[string]*Tier
	hasCold    bool
	promotable []string // backend ids of tiers with promote-on-read
}

// Primary returns the primary tier.
func (s *Snapshot) Primary() *Tier { return s.primary }

// HasCold reports whether any tier besides the primary store exists. Without
// one the tiered store is a plain pass-through to the primary store.
func (s *Snapshot) HasCold() bool { return s.hasCold }

// ByID returns the tier with the given id.
func (s *Snapshot) ByID(id string) (*Tier, bool) {
	t, ok := s.byID[id]

	return t, ok
}

// ByBackend returns the tier placed on the given backend ("" = primary).
func (s *Snapshot) ByBackend(backendID string) (*Tier, bool) {
	t, ok := s.byBackend[backendID]

	return t, ok
}

// PromoteBackends returns the backend ids of tiers that promote blobs on read.
func (s *Snapshot) PromoteBackends() []string { return s.promotable }

func newSnapshot(tiers []*Tier) *Snapshot {
	s := &Snapshot{Tiers: tiers, byID: make(map[string]*Tier, len(tiers)), byBackend: make(map[string]*Tier, len(tiers))}

	for _, t := range tiers {
		s.byID[t.ID] = t
		s.byBackend[t.BackendID] = t

		if t.Primary() {
			s.primary = t
		} else {
			s.hasCold = true

			if t.PromoteOnRead && t.Err == nil {
				s.promotable = append(s.promotable, t.BackendID)
			}
		}
	}

	return s
}

// cachedStore is an opened backend store, reused while the backend row is
// unchanged. Only successful opens are cached: a failed one (a mount that was
// late, a credential rotated) is retried on every refresh.
type cachedStore struct {
	fingerprint string
	store       storage.BlobStore
}

// Registry keeps the tier list and the opened backend stores. The list is
// reloaded from the database periodically (and on demand), so a tier created
// through one replica becomes usable on the others within the refresh interval.
type Registry struct {
	db         DB
	primary    storage.BlobStore
	primaryLoc location
	log        *logger.Logger

	snap atomic.Pointer[Snapshot]

	mu     sync.Mutex // serializes Refresh and guards stores and warned
	stores map[string]cachedStore
	warned map[string]string // backend id -> last open failure logged

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	started  bool
}

// NewRegistry creates a registry whose only tier, until the first Refresh, is
// the primary store. primaryCfg is the server's storage configuration; it is
// used to refuse a tier that aliases the primary store (same directory, bucket,
// container or repository).
func NewRegistry(db DB, primary storage.BlobStore, primaryCfg storage.StorageConfig, log *logger.Logger) *Registry {
	r := &Registry{
		db: db, primary: primary, log: log,
		stores: map[string]cachedStore{},
		warned: map[string]string{},
		stop:   make(chan struct{}), done: make(chan struct{}),
	}

	r.primaryLoc = locate(primaryCfg)

	r.snap.Store(newSnapshot([]*Tier{r.primaryTier()}))

	return r
}

func (r *Registry) primaryTier() *Tier {
	return &Tier{ID: "", Name: DefaultPrimaryTierName, Store: r.primary, identity: r.primaryLoc.id()}
}

// Snapshot returns the current tier view.
func (r *Registry) Snapshot() *Snapshot { return r.snap.Load() }

// Primary returns the primary store.
func (r *Registry) Primary() storage.BlobStore { return r.primary }

// Refresh reloads tiers and backends from the database. On a database error the
// previous snapshot stays in effect.
func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rows, err := r.db.ListStorageTiers(ctx)
	if err != nil {
		return fmt.Errorf("load storage tiers: %w", err)
	}

	backends, err := r.db.ListStorageBackends(ctx)
	if err != nil {
		return fmt.Errorf("load storage backends: %w", err)
	}

	byID := make(map[string]*models.StorageBackend, len(backends))
	for _, b := range backends {
		byID[b.ID] = b
	}

	tiers := make([]*Tier, 0, len(rows)+1)
	cl := newClaims()
	live := map[string]bool{}

	_ = cl.claim("", r.primaryLoc) // the first claim cannot conflict

	hasPrimary := false

	for _, row := range rows {
		t := &Tier{ID: row.ID, Name: row.Name, BackendID: row.BackendID, BackendName: row.BackendName, Kind: row.BackendKind, Rank: row.Rank, PromoteOnRead: row.PromoteOnRead}

		if t.Primary() {
			hasPrimary = true
			t.Store, t.identity = r.primary, r.primaryLoc.id()
		} else {
			live[t.BackendID] = true
			r.openBackend(t, byID[t.BackendID], cl)
		}

		tiers = append(tiers, t)
	}

	if !hasPrimary {
		// Pre-migration or hand-edited database: keep the primary store reachable.
		tiers = append([]*Tier{r.primaryTier()}, tiers...)
	}

	for id := range r.stores {
		if !live[id] {
			delete(r.stores, id)
		}
	}

	for id := range r.warned {
		if !live[id] {
			delete(r.warned, id)
		}
	}

	r.snap.Store(newSnapshot(tiers))

	return nil
}

// openBackend resolves t's store from its backend row. The backend's location
// is claimed first, so a backend aliasing the primary store or an earlier tier
// is refused before anything is created at it; the opened store is reused while
// the row is unchanged.
func (r *Registry) openBackend(t *Tier, b *models.StorageBackend, cl *claims) {
	if b == nil {
		t.Err = fmt.Errorf("%w: backend of tier %q is missing", ErrTierUnavailable, t.Name)

		return
	}

	t.BackendName, t.Kind = b.Name, b.Kind

	store, loc, err := r.resolveStore(b, cl)
	if err != nil {
		err = fmt.Errorf("%w: %s: %w", ErrTierUnavailable, t.Name, err)
		if msg := err.Error(); r.warned[b.ID] != msg {
			r.warned[b.ID] = msg

			if r.log != nil {
				r.log.Warnw("storage tier backend unavailable", "tier", t.Name, "backend", b.Name, "error", err)
			}
		}

		t.Err = err

		return
	}

	delete(r.warned, b.ID)

	t.Store, t.identity = store, loc.id()
}

func (r *Registry) resolveStore(b *models.StorageBackend, cl *claims) (storage.BlobStore, location, error) {
	cfg, err := ConfigFromBackend(b.Kind, b.Config)
	if err != nil {
		return nil, location{}, err
	}

	loc := locate(cfg)
	if err := cl.claim(b.ID, loc); err != nil {
		return nil, loc, err
	}

	sum := sha256.Sum256(append([]byte(b.Kind+"\x00"), b.Config...))
	fp := hex.EncodeToString(sum[:])

	if c, ok := r.stores[b.ID]; ok && c.fingerprint == fp {
		return c.store, loc, nil
	}

	store, err := storage.NewBlobStore(cfg)
	if err != nil {
		delete(r.stores, b.ID)

		return nil, loc, err
	}

	r.stores[b.ID] = cachedStore{fingerprint: fp, store: store}

	return store, loc, nil
}

// Start reloads the registry every interval until Stop. It does an initial
// Refresh first so tiers are usable before the first request.
func (r *Registry) Start(ctx context.Context, interval time.Duration) {
	if err := r.Refresh(ctx); err != nil && r.log != nil {
		r.log.Errorw("initial storage tier load failed", "error", err)
	}

	if interval <= 0 {
		close(r.done)

		return
	}

	r.started = true

	go func() { //nolint:gosec // G118: background refresher; each reload gets its own bounded context
		defer close(r.done)

		t := time.NewTicker(interval)
		defer t.Stop()

		for {
			select {
			case <-t.C:
				rctx, cancel := context.WithTimeout(context.Background(), interval)
				if err := r.Refresh(rctx); err != nil && r.log != nil {
					r.log.Warnw("storage tier reload failed", "error", err)
				}

				cancel()
			case <-r.stop:
				return
			}
		}
	}()
}

// Stop ends the periodic reload. It is idempotent.
func (r *Registry) Stop() {
	r.stopOnce.Do(func() { close(r.stop) })

	if r.started {
		<-r.done
	}
}

// ConfigFromBackend converts a stored storage_backends row to a storage
// configuration: it accepts the camelCase keys the admin UI writes and the
// snake_case ones API clients use, and decrypts secret fields.
func ConfigFromBackend(kind string, raw []byte) (storage.StorageConfig, error) {
	cfg := map[string]any{}

	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return storage.StorageConfig{}, fmt.Errorf("%w: not a JSON object", errBackendConfig)
		}
	}

	str := func(keys ...string) string {
		for _, k := range keys {
			if s, _ := cfg[k].(string); strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}

		return ""
	}

	secret := func(keys ...string) (string, error) {
		v := str(keys...)
		if v == "" {
			return "", nil
		}

		plain, err := crypto.Decrypt(v)
		if err != nil {
			return "", fmt.Errorf("%w: stored credentials cannot be decrypted", errBackendConfig)
		}

		return plain, nil
	}

	flag := func(keys ...string) bool {
		for _, k := range keys {
			if b, ok := cfg[k].(bool); ok && b {
				return true
			}
		}

		return false
	}

	out := storage.StorageConfig{Type: kind}

	var err error

	switch kind {
	case "filesystem":
		out.Path = str("path")
		if out.Path == "" {
			return out, fmt.Errorf("%w: path is required", errBackendConfig)
		}
	case "s3", "garage", "seaweedfs":
		out.Endpoint, out.Bucket, out.Region = str("endpoint"), str("bucket"), str("region")
		out.ForcePathStyle = out.Endpoint != "" || flag("pathStyle", "force_path_style", "forcePathStyle")

		if out.AccessKey, err = secret("access_key", "accessKey"); err != nil {
			return out, err
		}

		if out.SecretKey, err = secret("secret_key", "secretKey"); err != nil {
			return out, err
		}
	case "oci":
		out.Endpoint = ociEndpoint(str("endpoint"), flag("insecure", "oci_insecure", "ociInsecure"))
		out.Bucket = str("bucket", "repository")
		out.OCIInsecure = flag("insecure", "oci_insecure", "ociInsecure")
		out.OCIUsername = str("username")
		out.OCIPassword, err = secret("password")
	case "gcs":
		err = gcsConfig(&out, str, secret)
	case "azure":
		out.AzureAccountName = str("account", "account_name", "accountName")
		out.AzureContainerName = str("container")
		out.AzureAccountKey, err = secret("account_key", "accountKey")
	default:
		return out, fmt.Errorf("%w: unsupported kind %q", errBackendConfig, kind)
	}

	return out, err
}

func gcsConfig(out *storage.StorageConfig, str func(...string) string, secret func(...string) (string, error)) error {
	var err error

	out.GCSBucket = str("bucket")
	out.Bucket = out.GCSBucket

	if out.GCSHMACKey, err = secret("hmac_key", "hmacKey"); err != nil {
		return err
	}

	out.GCSHMACSecret, err = secret("hmac_secret", "hmacSecret")

	return err
}

// ociEndpoint completes a bare host[:port] to an absolute URL.
func ociEndpoint(endpoint string, insecure bool) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" || strings.Contains(endpoint, "://") {
		return endpoint
	}

	if insecure {
		return "http://" + endpoint
	}

	return "https://" + endpoint
}

// PlacementTier returns the tier a blob placed on backendID belongs to.
func (r *Registry) PlacementTier(backendID string) (*Tier, error) {
	t, ok := r.Snapshot().ByBackend(backendID)
	if !ok {
		return nil, fmt.Errorf("%w: backend %q", ErrUnknownTier, backendID)
	}

	return t, nil
}
