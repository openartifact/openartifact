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
)

// Snippet is the configure/publish commands for one client tool.
type Snippet struct {
	Client    string `json:"client"`
	Configure string `json:"configure"`
	Publish   string `json:"publish,omitempty"`
}

// target carries the values every snippet template needs.
type target struct {
	origin string // server origin without trailing slash
	host   string // host[:port] of origin
	name   string // host of origin without the port (what sbt matches credentials on)
	repo   string // raw repository name
	esc    string // URL-path-escaped repository name
	base   string // native repository base URL
}

// isolatedPrefix reports whether a format is served under /repos/{format}/{repo}.
func isolatedPrefix(format string) bool {
	switch format {
	case "maven", "sbt", "ivy", "raw":
		return true
	default:
		return false
	}
}

// RepoBaseURL returns the native base URL of a repository for its format (no
// trailing slash). Docker serves the registry API at the host root. origin
// must not end in a slash.
func RepoBaseURL(format, origin, repo string) string {
	name := url.PathEscape(repo)

	switch {
	case format == "docker":
		return origin
	case isolatedPrefix(format):
		return origin + "/repos/" + format + "/" + name
	default:
		return origin + "/repos/" + name
	}
}

// Snippets returns the configure/publish snippets for a repository of the
// given format. Unknown formats return a single bare-URL entry.
func Snippets(format, origin, repo string) []Snippet {
	origin = strings.TrimRight(origin, "/")
	c := target{
		origin: origin,
		host:   hostOf(origin),
		name:   hostnameOf(origin),
		repo:   repo,
		esc:    url.PathEscape(repo),
		base:   RepoBaseURL(format, origin, repo),
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
			Configure: fmt.Sprintf("# ~/.pypirc\n[distutils]\nindex-servers = openartifact\n\n[openartifact]\nrepository = %s/\nusername = <USERNAME>\npassword = <TOKEN>",
				c.base),
			Publish: "twine upload --repository openartifact dist/*",
		},
	}
}

func mavenSnippets(c target) []Snippet {
	return []Snippet{{
		Client: "Maven",
		Configure: fmt.Sprintf("<!-- ~/.m2/settings.xml -->\n<settings>\n  <servers>\n    <server>\n      <id>%s</id>\n      <username><USERNAME></username>\n      <password><TOKEN></password>\n    </server>\n  </servers>\n</settings>",
			c.repo),
		Publish: fmt.Sprintf("mvn deploy -DaltDeploymentRepository=%s::default::%s", c.repo, c.base),
	}}
}

func helmSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "helm",
		Configure: fmt.Sprintf("helm repo add %s %s --username <USERNAME> --password <TOKEN>\nhelm repo update", c.repo, c.base),
		Publish:   fmt.Sprintf("curl -u <USERNAME>:<TOKEN> --data-binary @my-chart-0.1.0.tgz %s/charts", c.base),
	}}
}

func debSnippets(c target) []Snippet {
	return []Snippet{{
		Client: "apt",
		Configure: fmt.Sprintf("curl -fsSL %[1]s/public.key | sudo gpg --dearmor -o /etc/apt/keyrings/%[2]s.gpg\necho \"deb [signed-by=/etc/apt/keyrings/%[2]s.gpg] %[1]s stable main\" | sudo tee /etc/apt/sources.list.d/%[2]s.list\nsudo apt update",
			c.base, c.repo),
		Publish: fmt.Sprintf("curl -u <USERNAME>:<TOKEN> -F file=@my-package_1.0_amd64.deb -F distribution=stable -F component=main %s/api/v1/repositories/%s/debs",
			c.origin, c.esc),
	}}
}

func rpmSnippets(c target) []Snippet {
	return []Snippet{{
		Client: "dnf / yum",
		Configure: fmt.Sprintf("# /etc/yum.repos.d/%[1]s.repo\n[%[1]s]\nname=%[1]s\nbaseurl=%[2]s\nenabled=1\nrepo_gpgcheck=1\ngpgkey=%[2]s/public.key",
			c.repo, c.base),
		Publish: fmt.Sprintf("curl -u <USERNAME>:<TOKEN> -F file=@my-package-1.0.x86_64.rpm %s/api/v1/repositories/%s/packages",
			c.origin, c.esc),
	}}
}

func cargoSnippets(c target) []Snippet {
	return []Snippet{{
		Client: "cargo",
		Configure: fmt.Sprintf("# ~/.cargo/config.toml\n[registries.%s]\nindex = \"sparse+%s/index/\"\ntoken = \"<TOKEN>\"",
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
	return []Snippet{{
		Client: "dotnet",
		Configure: fmt.Sprintf("dotnet nuget add source %s/v3/index.json --name %s --username <USERNAME> --password <TOKEN> --store-password-in-clear-text",
			c.base, c.repo),
		Publish: fmt.Sprintf("dotnet nuget push my-package.1.0.0.nupkg --source %s --api-key <TOKEN>", c.repo),
	}}
}

func rubygemsSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "gem",
		Configure: fmt.Sprintf("gem sources --add %s/", c.base),
		Publish:   "GEM_HOST_API_KEY=<TOKEN> gem push my-gem-1.0.0.gem --host " + c.base,
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
	return []Snippet{{
		Client:    "conda",
		Configure: "conda config --add channels " + c.base,
		Publish: fmt.Sprintf("curl -u <USERNAME>:<TOKEN> -H \"X-Filename: my-package-1.0-0.conda\" --data-binary @my-package-1.0-0.conda %s/linux-64",
			c.base),
	}}
}

func conanSnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "conan",
		Configure: fmt.Sprintf("conan remote add %[1]s %[2]s\nconan remote login %[1]s <USERNAME> -p <TOKEN>", c.repo, c.base),
		Publish:   fmt.Sprintf("conan upload \"my-lib/1.0\" -r %s --confirm", c.repo),
	}}
}

func sbtSnippets(c target) []Snippet {
	return []Snippet{{
		Client: "sbt",
		Configure: fmt.Sprintf("// build.sbt\nresolvers += \"%[1]s\" at \"%[2]s\"\ncredentials += Credentials(\"openartifact\", \"%[3]s\", \"<USERNAME>\", \"<TOKEN>\")",
			c.repo, c.base, c.name),
		Publish: fmt.Sprintf("ThisBuild / publishTo := Some(\"%s\" at \"%s\")\n// then: sbt publish", c.repo, c.base),
	}}
}

func ivySnippets(c target) []Snippet {
	return []Snippet{{
		Client:    "Ivy",
		Configure: fmt.Sprintf("<ibiblio name=\"%s\" m2compatible=\"true\" root=\"%s\"/>", c.repo, c.base),
		Publish:   fmt.Sprintf("<ivy:publish resolver=\"%s\" pubrevision=\"1.0\" overwrite=\"true\">\n  <artifacts pattern=\"dist/[artifact]-[revision].[ext]\"/>\n</ivy:publish>", c.repo),
	}}
}
