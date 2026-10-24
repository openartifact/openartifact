// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import "fmt"

// Config is the branding.* configuration (YAML) / OPENARTIFACT_BRANDING_*
// environment: the defaults an administrator can override at runtime (see
// docs/operations/branding.md). Image values are file paths read at startup.
type Config struct {
	// ProductName replaces "OpenArtifact" in the UI (title, sidebar, login).
	ProductName string `yaml:"productName"`
	// LogoLight is the path of a PNG or SVG logo shown on light surfaces; when
	// only one logo is set it is used for both themes.
	LogoLight string `yaml:"logoLight"`
	// LogoDark is the path of a PNG or SVG logo shown on dark surfaces.
	LogoDark string `yaml:"logoDark"`
	// Favicon is a PNG or SVG file.
	Favicon string `yaml:"favicon"`
	// PrimaryColor is the accent colour as #rrggbb.
	PrimaryColor string `yaml:"primaryColor"`
	// LoginMessage is shown on the login page (plain text with a tiny markdown
	// subset).
	LoginMessage string `yaml:"loginMessage"`
	// SupportURL is an http(s) or mailto link shown in the About dialog.
	SupportURL string `yaml:"supportUrl"`
	// SourceURL is where users can get the source of the running version
	// (AGPL-3.0 section 13); it defaults to the upstream repository and cannot
	// be hidden.
	SourceURL string `yaml:"sourceUrl"`
}

// Defaults is the validated, loaded form of Config.
type Defaults struct {
	ProductName  string
	PrimaryColor string
	LoginMessage string
	SupportURL   string
	SourceURL    string
	Images       map[Kind]*Image
}

// Validate checks the text values; the image files are read by Defaults.
func (c Config) Validate() error {
	_, err := c.normalised()

	return err
}

func (c Config) normalised() (Defaults, error) {
	var (
		d   Defaults
		err error
	)

	if c.ProductName == "" {
		d.ProductName = DefaultProductName
	} else if d.ProductName, err = ValidateProductName(c.ProductName); err != nil {
		return Defaults{}, fmt.Errorf("branding.productName: %w", err)
	}

	if d.PrimaryColor, err = ValidateColor(c.PrimaryColor); err != nil {
		return Defaults{}, fmt.Errorf("branding.primaryColor: %w", err)
	}

	if d.LoginMessage, err = ValidateLoginMessage(c.LoginMessage); err != nil {
		return Defaults{}, fmt.Errorf("branding.loginMessage: %w", err)
	}

	if d.SupportURL, err = ValidateSupportURL(c.SupportURL); err != nil {
		return Defaults{}, fmt.Errorf("branding.supportUrl: %w", err)
	}

	if d.SourceURL, err = ValidateSourceURL(c.SourceURL); err != nil {
		return Defaults{}, fmt.Errorf("branding.sourceUrl: %w", err)
	}

	return d, nil
}

// Defaults validates the configuration and loads the image files. Any
// failure is fatal at startup: a configured logo that cannot be shown must not
// be silently replaced by another one.
func (c Config) Defaults() (Defaults, error) {
	d, err := c.normalised()
	if err != nil {
		return Defaults{}, err
	}

	d.Images = make(map[Kind]*Image)

	for kind, path := range map[Kind]string{LogoLight: c.LogoLight, LogoDark: c.LogoDark, Favicon: c.Favicon} {
		if path == "" {
			continue
		}

		img, err := LoadImageFile(path)
		if err != nil {
			return Defaults{}, fmt.Errorf("branding.%s: %w", configKey(kind), err)
		}

		d.Images[kind] = img
	}

	return d, nil
}

func configKey(k Kind) string {
	switch k {
	case LogoLight:
		return "logoLight"
	case LogoDark:
		return "logoDark"
	default:
		return "favicon"
	}
}
