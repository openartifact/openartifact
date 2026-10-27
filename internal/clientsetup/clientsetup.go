// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package clientsetup builds native client URLs and per-client configure /
// publish snippets for a repository. It is the Go port of `repoBaseUrl` and
// `clientSetup` in web/src/lib/installCommands.ts; keep the two in sync.
// Credentials are always the literal `<USERNAME>` / `<TOKEN>` placeholders.
package clientsetup

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/repotype"
)

// Snippet is the configure/publish commands for one client tool.
type Snippet struct {
	Client    string `json:"client"`
	Configure string `json:"configure"`
	Publish   string `json:"publish,omitempty"`
}

// target carries the values every snippet template needs.
type target struct {
	origin   string // server origin without trailing slash
	host     string // host[:port] of origin
	name     string // host of origin without the port (what sbt matches credentials on)
	repo     string // raw repository name
	esc      string // URL-path-escaped repository name
	base     string // native repository base URL
	repoType string // hosted, proxy or group
}

// RepoBaseURL returns the native base URL of a repository for its format (no
// trailing slash). Docker serves the registry API at the host root. origin
// must not end in a slash.
func RepoBaseURL(format, origin, repo string) string {
	name := url.PathEscape(repo)

	switch {
	case format == "docker":
		return origin
	case repotype.Isolated(format):
		return origin + "/repos/" + format + "/" + name
	default:
		return origin + "/repos/" + name
	}
}

// Snippets returns the configure/publish snippets for a repository of the
// given format and type ("hosted", "proxy" or "group"; "" is hosted). Unknown
// formats return a single bare-URL entry.
func Snippets(format, origin, repo, repoType string) []Snippet {
	origin = strings.TrimRight(origin, "/")
	c := target{
		origin:   origin,
		host:     hostOf(origin),
		name:     hostnameOf(origin),
		repo:     repo,
		esc:      url.PathEscape(repo),
		base:     RepoBaseURL(format, origin, repo),
		repoType: repoType,
	}

	if build := builderFor(format); build != nil {
		return build(c)
	}

	return []Snippet{{Client: "URL", Configure: c.base + "/"}}
}

func hostOf(origin string) string {
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		return u.Host
	}

	origin = strings.TrimPrefix(origin, "https://")

	return strings.TrimPrefix(origin, "http://")
}

// hostnameOf is the host of origin without its port, as java.net.URL.getHost
// reports it (an IPv6 literal keeps its brackets).
func hostnameOf(origin string) string {
	if u, err := url.Parse(origin); err == nil && u.Hostname() != "" {
		h := u.Hostname()
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}

		return h
	}

	host := hostOf(origin)
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}

	return host
}

// builderFor returns the snippet builder for a format, or nil when unknown.
//
//nolint:cyclop // one case per format
func builderFor(format string) func(target) []Snippet {
	switch format {
	case "npm":
		return npmSnippets
	case "docker":
		return dockerSnippets
	case "pypi":
		return pypiSnippets
	case "maven":
		return mavenSnippets
	case "helm":
		return helmSnippets
	case "deb":
		return debSnippets
	case "rpm":
		return rpmSnippets
	case "pacman":
		return pacmanSnippets
	case "cargo":
		return cargoSnippets
	case "go":
		return gomodSnippets
	case "nuget":
		return nugetSnippets
	case "rubygems":
		return rubygemsSnippets
	case "raw":
		return rawSnippets
	case "conda":
		return condaSnippets
	case "conan":
		return conanSnippets
	case "apk":
		return apkSnippets
	case "terraform":
		return terraformSnippets
	case "sbt":
		return sbtSnippets
	case "ivy":
		return ivySnippets
	default:
		return nil
	}
}

func npmSnippets(c target) []Snippet {
	auth := fmt.Sprintf("//%s/repos/%s/:_authToken <TOKEN>", c.host, c.esc)

	return []Snippet{
		{
			Client:    "npm",
			Configure: fmt.Sprintf("npm config set registry %s/\nnpm config set %s", c.base, auth),
			Publish:   fmt.Sprintf("npm publish --registry %s/", c.base),
		},
		{
			Client:    "pnpm",
			Configure: fmt.Sprintf("pnpm config set registry %s/\npnpm config set %s", c.base, auth),
			Publish:   fmt.Sprintf("pnpm publish --registry %s/", c.base),
		},
		{
			Client:    "yarn",
			Configure: fmt.Sprintf("yarn config set npmRegistryServer %s/\nyarn config set npmAuthToken <TOKEN>", c.base),
			Publish:   "yarn npm publish",
		},
	}
}

func dockerSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "docker",
		Configure: fmt.Sprintf("docker login %s -u <USERNAME> -p <TOKEN>", c.host),
		Publish: fmt.Sprintf("docker tag my-image:latest %[1]s/%[2]s:latest\ndocker push %[1]s/%[2]s:latest",
			c.host, c.repo),
	}}
}

func pypiSnippets(c target) []Snippet {
	return []Snippet{
		{
			Client:    "pip",
			Configure: fmt.Sprintf("pip config set global.index-url %s/simple/", c.base),
		},
		{
			Client: "twine",
			Configure: fmt.Sprintf(
				"# ~/.pypirc\n[distutils]\nindex-servers = openartifact\n\n[openartifact]\n"+
					"repository = %s/legacy/\nusername = <USERNAME>\npassword = <TOKEN>",
				c.base,
			),
			Publish: "twine upload --repository openartifact dist/*",
		},
	}
}

func mavenSnippets(c target) []Snippet {
	return []Snippet{
		{
			Client: "Maven",
			Configure: fmt.Sprintf(
				"<!-- ~/.m2/settings.xml -->\n<settings>\n  <servers>\n    <server>\n      <id>%s</id>\n"+
					"      <username><USERNAME></username>\n      <password><TOKEN></password>\n"+
					"    </server>\n  </servers>\n</settings>",
				c.repo,
			),
			Publish: fmt.Sprintf("mvn deploy -DaltDeploymentRepository=%s::default::%s", c.repo, c.base),
		},
	}
}

func helmSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "helm",
		Configure: fmt.Sprintf("helm repo add %s %s --username <USERNAME> --password <TOKEN>\nhelm repo update", c.repo, c.base),
		Publish:   fmt.Sprintf("curl -u <USERNAME>:<TOKEN> --data-binary @my-chart-0.1.0.tgz %s/charts", c.base),
	}}
}

func debSnippets(c target) []Snippet {
	return []Snippet{
		{
			Client: "apt",
			Configure: fmt.Sprintf(
				"curl -fsSL %[1]s/public.key | sudo gpg --dearmor -o /etc/apt/keyrings/%[2]s.gpg\n"+
					"echo \"deb [signed-by=/etc/apt/keyrings/%[2]s.gpg] %[1]s stable main\" | "+
					"sudo tee /etc/apt/sources.list.d/%[2]s.list\nsudo apt update",
				c.base,
				c.repo,
			),
			Publish: fmt.Sprintf(
				"curl -u <USERNAME>:<TOKEN> -F file=@my-package_1.0_amd64.deb -F distribution=stable -F component=main %s/api/v1/repositories/%s/debs",
				c.origin,
				c.esc,
			),
		},
	}
}

func rpmSnippets(c target) []Snippet {
	return []Snippet{
		{
			Client: "dnf / yum",
			Configure: fmt.Sprintf(
				"# /etc/yum.repos.d/%[1]s.repo\n[%[1]s]\nname=%[1]s\nbaseurl=%[2]s\nenabled=1\nrepo_gpgcheck=1\ngpgkey=%[2]s/public.key",
				c.repo,
				c.base,
			),
			Publish: fmt.Sprintf("curl -u <USERNAME>:<TOKEN> -F file=@my-package-1.0.x86_64.rpm %s/api/v1/repositories/%s/packages",
				c.origin, c.esc),
		},
	}
}

// pacmanSnippets configures /etc/pacman.conf, trusts the repository key and
// publishes a package (and optionally its detached signature) with a token PUT.
// A group is read-only: it has nothing to publish, and its packages keep the
// signatures of the members that hold them.
func pacmanSnippets(c target) []Snippet {
	if c.repoType == "proxy" {
		return pacmanProxySnippets(c)
	}

	key := c.base + "/public.key"
	pkg := "my-package-1.0-1-x86_64.pkg.tar.zst"
	put := func(file string) string {
		return fmt.Sprintf("curl -fsS -H \"Authorization: Bearer $OA_TOKEN\" -T %[1]s %[2]s/os/x86_64/%[1]s", file, c.base)
	}

	configure := fmt.Sprintf("# /etc/pacman.conf\n[%s]\nSigLevel = Required\nServer = %s/os/$arch", c.repo, c.base) +
		"\n\n# private repository: Server = https://<USER>:<TOKEN>@" + c.host + "/repos/" + c.esc + "/os/$arch" +
		"\n\n# trust the repository key\ncurl -fsSL " + key + " | sudo pacman-key --add -" +
		"\nsudo pacman-key --lsign-key $(curl -fsSL " + key +
		" | gpg --show-keys --with-colons | awk -F: '/^fpr/{print $10; exit}')"

	if c.repoType == "group" {
		return []Snippet{{
			Client: "pacman",
			Configure: configure + "\n\n# packages keep the signatures of the members that hold them: trust each hosted member's key" +
				" too (it is the group's when they share the active key), for example\n# curl -fsSL " + c.origin + "/repos/<member>/public.key" +
				" | sudo pacman-key --add -",
		}}
	}

	return []Snippet{{
		Client:    "pacman",
		Configure: configure,
		Publish:   put(pkg) + "\n\n# optional detached signature\n" + put(pkg+".sig"),
	}}
}

// pacmanProxySnippets points pacman at a proxy repository, which mirrors an
// Arch mirror root: paths pass through, so the server line carries the
// $repo/os/$arch layout. Signatures are the upstream's (archlinux-keyring), so
// there is no key to import from this server and nothing to publish.
func pacmanProxySnippets(c target) []Snippet {
	return []Snippet{{
		Client: "pacman",
		Configure: "# top of /etc/pacman.d/mirrorlist\nServer = " + c.base + "/$repo/os/$arch" +
			"\n\n# private repository: Server = https://<USER>:<TOKEN>@" + c.host + "/repos/" + c.esc + "/$repo/os/$arch" +
			"\n\nsudo pacman -Syu",
	}}
}

func apkSnippets(c target) []Snippet {
	authBase := c.base
	if i := strings.Index(c.base, "://"); i >= 0 {
		authBase = c.base[:i+3] + "<USERNAME>:<TOKEN>@" + c.base[i+3:]
	}

	if c.repoType == "proxy" {
		// The index keeps the upstream's signature, so no key comes from this
		// server: the client verifies it with the keys it already trusts.
		return []Snippet{{
			Client: "apk",
			Configure: fmt.Sprintf(
				"# the mirror's own signatures are kept: Alpine's keys (alpine-keys) are already installed in official images\n"+
					"# private repository: put the credentials in the URL, %[2]s/v3.20/main\n"+
					"echo \"%[1]s/v3.20/main\" >> /etc/apk/repositories\n"+
					"echo \"%[1]s/v3.20/community\" >> /etc/apk/repositories\napk update\napk add PACKAGE",
				c.base, authBase,
			),
		}}
	}

	if c.repoType == "group" {
		// Read-only: the merged indexes are signed with the group's own key,
		// not its members'.
		return []Snippet{{
			Client: "apk",
			Configure: fmt.Sprintf(
				"# the group signs its indexes with its own key, not its members'\n"+
					"# private repository: put the credentials in the URL, %[3]s/v3.20/main, "+
					"and fetch the key with wget --user <USERNAME> --password <TOKEN>\n"+
					"wget -O /etc/apk/keys/openartifact-%[2]s.rsa.pub %[1]s/public.key\n"+
					"echo \"%[1]s/v3.20/main\" >> /etc/apk/repositories\napk update\napk add PACKAGE",
				c.base, c.repo, authBase,
			),
		}}
	}

	return []Snippet{{
		Client: "apk",
		Configure: fmt.Sprintf(
			"# private repository: put the credentials in the URL, %[3]s/v3.20/main, "+
				"and fetch the key with wget --user <USERNAME> --password <TOKEN>\n"+
				"wget -O /etc/apk/keys/openartifact-%[2]s.rsa.pub %[1]s/public.key\n"+
				"echo \"%[1]s/v3.20/main\" >> /etc/apk/repositories\napk update\napk add PACKAGE",
			c.base, c.repo, authBase,
		),
		Publish: fmt.Sprintf(
			"curl -T my-pkg-1.0-r0.apk -H \"Authorization: Bearer <TOKEN>\" %[1]s/v3.20/main/x86_64/my-pkg-1.0-r0.apk\n\n"+
				"# or through the REST API\n"+
				"curl -H \"Authorization: Bearer <TOKEN>\" -F file=@my-pkg-1.0-r0.apk -F branch=v3.20 -F repo=main "+
				"%[2]s/api/v1/repositories/%[3]s/apks",
			c.base, c.origin, c.esc,
		),
	}}
}

func cargoSnippets(c target) []Snippet {
	return []Snippet{{
		Client: "cargo",
		Configure: fmt.Sprintf("# ~/.cargo/config.toml\n[registries.%s]\nindex = \"sparse+%s/index/\"\ntoken = \"Bearer <TOKEN>\"",
			c.repo, c.base),
		Publish: "cargo publish --registry " + c.repo,
	}}
}

func gomodSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "go",
		Configure: fmt.Sprintf("go env -w GOPROXY=%s,direct\ngo env -w GONOSUMDB=*", c.base),
	}}
}

func nugetSnippets(c target) []Snippet {
	return []Snippet{
		{
			Client: "dotnet",
			Configure: fmt.Sprintf(
				"dotnet nuget add source %s/v3/index.json --name %s --username <USERNAME> --password <TOKEN> --store-password-in-clear-text",
				c.base,
				c.repo,
			),
			Publish: fmt.Sprintf("dotnet nuget push my-package.1.0.0.nupkg --source %s --api-key <TOKEN>", c.repo),
		},
	}
}

func rubygemsSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "gem",
		Configure: fmt.Sprintf("gem sources --add %s/", c.base),
		Publish:   "GEM_HOST_API_KEY=\"Bearer <TOKEN>\" gem push my-gem-1.0.0.gem --host " + c.base,
	}}
}

func rawSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "curl",
		Configure: fmt.Sprintf("curl -fO %s/path/to/file.bin", c.base),
		Publish:   fmt.Sprintf("curl -u <USERNAME>:<TOKEN> -T file.bin %s/path/to/file.bin", c.base),
	}}
}

func condaSnippets(c target) []Snippet {
	return []Snippet{
		{
			Client:    "conda",
			Configure: "conda config --add channels " + c.base,
			Publish: fmt.Sprintf(
				"curl -u <USERNAME>:<TOKEN> -H \"X-Filename: my-package-1.0-0.conda\" --data-binary @my-package-1.0-0.conda %s/linux-64",
				c.base,
			),
		},
	}
}

func conanSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "conan",
		Configure: fmt.Sprintf("conan remote add %[1]s %[2]s\nconan remote login %[1]s <USERNAME> -p <TOKEN>", c.repo, c.base),
		Publish:   fmt.Sprintf("conan upload \"my-lib/1.0\" -r %s --confirm", c.repo),
	}}
}

// terraformSnippets configures both terraform and OpenTofu: the registry
// namespace is the repository name, and the publish API is OpenArtifact's own
// (no upstream protocol exists for uploads).
func terraformSnippets(c target) []Snippet {
	const note = "# terraform and tofu need HTTPS and a hostname with a dot"

	usage := fmt.Sprintf(
		"\n\n# modules from this repository\nmodule \"x\" {\n  source  = \"%[1]s/%[2]s/NAME/SYSTEM\"\n  version = \"1.0.0\"\n}"+
			"\n\n# provider\nterraform {\n  required_providers {\n    TYPE = {\n      source  = \"%[1]s/%[2]s/TYPE\"\n"+
			"      version = \"1.0.0\"\n    }\n  }\n}",
		c.host, c.repo,
	)
	creds := func(file string) string {
		return fmt.Sprintf("%s\n# %s\ncredentials \"%s\" {\n  token = \"<TOKEN>\"\n}", note, file, c.host)
	}
	api := c.base + "/_api"
	publish := fmt.Sprintf(
		"# module archive (.tar.gz or .zip)\n"+
			"curl -X PUT -H \"Authorization: Bearer <TOKEN>\" --data-binary @module.tar.gz %[1]s/modules/NAME/SYSTEM/1.0.0"+
			"\n\n# provider zip, one per platform\n"+
			"curl -X PUT -H \"Authorization: Bearer <TOKEN>\" --data-binary @provider.zip %[1]s/providers/TYPE/1.0.0/linux/amd64",
		api,
	)

	return []Snippet{
		{Client: "terraform", Configure: creds("~/.terraformrc") + usage, Publish: publish},
		{Client: "OpenTofu", Configure: creds("~/.tofurc") + usage, Publish: publish},
	}
}

func sbtSnippets(c target) []Snippet {
	return []Snippet{
		{
			Client: "sbt",
			Configure: fmt.Sprintf(
				"// build.sbt\nresolvers += \"%[1]s\" at \"%[2]s\"\n"+
					"credentials += Credentials(\"openartifact\", \"%[3]s\", \"<USERNAME>\", \"<TOKEN>\")",
				c.repo,
				c.base,
				c.name,
			),
			Publish: fmt.Sprintf("ThisBuild / publishTo := Some(\"%s\" at \"%s\")\n// then: sbt publish", c.repo, c.base),
		},
	}
}

func ivySnippets(c target) []Snippet {
	return []Snippet{
		{
			Client:    "Ivy",
			Configure: fmt.Sprintf("<ibiblio name=\"%s\" m2compatible=\"true\" root=\"%s\"/>", c.repo, c.base),
			Publish: fmt.Sprintf(
				"<ivy:publish resolver=\"%s\" pubrevision=\"1.0\" overwrite=\"true\">\n"+
					"  <artifacts pattern=\"dist/[artifact]-[revision].[ext]\"/>\n</ivy:publish>",
				c.repo,
			),
		},
	}
}
