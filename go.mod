module petalbook.app/server

go 1.27

// The web app's npm packages are not Go packages, even when one ships a .go file.
ignore ./web/node_modules

require (
	github.com/KazuhaHub/authcore v0.6.3-0.20261106114224-f3886de2dcef
	github.com/crewjam/saml v0.5.1
	github.com/descope/virtualwebauthn v1.0.5
	github.com/go-jose/go-jose/v4 v4.1.9
	github.com/go-webauthn/webauthn v0.18.2
	github.com/google/jsonschema-go v0.4.3
	github.com/jackc/pgx/v5 v5.13.0
	github.com/mattermost/xml-roundtrip-validator v0.1.0
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/russellhaering/goxmldsig v1.6.1
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.1.0
	github.com/tats-u/goldmark-cjk-friendly/v2 v2.0.3
	github.com/yuin/goldmark v1.8.7
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/crypto v0.59.0
	golang.org/x/oauth2 v0.38.0
	golang.org/x/text v0.44.0
	modernc.org/sqlite v1.63.0
)

require (
	github.com/beevik/etree v1.7.0 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.4 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.4 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mojocn/base64Captcha v1.3.8 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/image v0.23.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	modernc.org/libc v1.82.12 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.13.0 // indirect
)

// authcore's saml package needs this pinned fork of crewjam/saml (see its go.mod).
replace github.com/crewjam/saml => github.com/openctemio/saml v0.5.2-0.20261106014128-2b3cdad337cd
