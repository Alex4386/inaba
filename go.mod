module github.com/Alex4386/inaba

go 1.27.3

tool (
	connectrpc.com/connect/v2/cmd/protoc-gen-connect-go
	github.com/google/gnostic/cmd/protoc-gen-openapi
	google.golang.org/protobuf/cmd/protoc-gen-go
)

require (
	connectrpc.com/connect/v2 v2.0.0
	connectrpc.com/vanguard v0.5.0
	github.com/SherClockHolmes/webpush-go v1.4.0
	github.com/coder/acp-go-sdk v0.20.0
	github.com/coder/websocket v1.8.15
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/creack/pty v1.1.24
	github.com/glebarez/sqlite v1.11.0
	github.com/go-rod/rod v0.116.2
	github.com/go-webauthn/webauthn v0.18.2
	github.com/hashicorp/yamux v0.1.2
	github.com/knadh/koanf/parsers/json v1.0.1
	github.com/knadh/koanf/parsers/yaml v1.1.1
	github.com/knadh/koanf/providers/confmap v1.0.1
	github.com/knadh/koanf/providers/env/v2 v2.0.2
	github.com/knadh/koanf/providers/file v1.2.1
	github.com/knadh/koanf/providers/structs v1.0.1
	github.com/knadh/koanf/v2 v2.4.1
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/pion/datachannel v1.6.4
	github.com/pion/stun/v3 v3.1.7
	github.com/pion/webrtc/v4 v4.2.29
	github.com/pquerna/otp v1.5.0
	github.com/shirou/gopsutil/v4 v4.26.10
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.9
	go.uber.org/fx v1.24.0
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/crypto v0.59.0
	golang.org/x/oauth2 v0.38.0
	golang.org/x/sys v0.50.0
	golang.org/x/time v0.15.0
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa
	google.golang.org/protobuf v1.36.12
	gorm.io/gorm v1.31.2
)

require (
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.11.2 // indirect
	github.com/fatih/structs v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.4 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/gnostic v0.7.1 // indirect
	github.com/google/gnostic-models v0.7.0 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/knadh/koanf/maps v0.1.2 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pion/dtls/v3 v3.1.16 // indirect
	github.com/pion/ice/v4 v4.4.9 // indirect
	github.com/pion/interceptor v0.1.51 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.2.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.20 // indirect
	github.com/pion/rtp v1.10.6 // indirect
	github.com/pion/sctp v1.12.1 // indirect
	github.com/pion/sdp/v3 v3.0.20 // indirect
	github.com/pion/srtp/v3 v3.1.3 // indirect
	github.com/pion/stun/v4 v4.0.1 // indirect
	github.com/pion/transport/v4 v4.1.0 // indirect
	github.com/pion/transport/v5 v5.1.5 // indirect
	github.com/pion/turn/v5 v5.1.3 // indirect
	github.com/power-devops/perfstat v0.0.0-20260805114148-88456608a4f6 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/tklauser/go-sysconf v0.4.0 // indirect
	github.com/tklauser/numcpus v0.12.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/ysmood/fetchup v0.2.3 // indirect
	github.com/ysmood/goob v0.4.0 // indirect
	github.com/ysmood/got v0.40.0 // indirect
	github.com/ysmood/gson v0.7.3 // indirect
	github.com/ysmood/leakless v0.9.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.uber.org/dig v1.19.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.26.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	modernc.org/libc v1.22.5 // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.5.0 // indirect
	modernc.org/sqlite v1.23.1 // indirect
)
