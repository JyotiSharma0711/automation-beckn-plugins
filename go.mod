module github.com/ONDC-Official/automation-beckn-plugins

go 1.25.5

replace github.com/beckn-one/beckn-onix => github.com/ONDC-Official/automation-beckn-onix v1.5.0

replace google.golang.org/protobuf => google.golang.org/protobuf v1.32.0

replace golang.org/x/sys => golang.org/x/sys v0.38.0

replace golang.org/x/text => golang.org/x/text v0.32.0

replace validationpkg => ./ondc-validator/validationpkg

replace go.opentelemetry.io/otel => go.opentelemetry.io/otel v1.38.0

replace go.opentelemetry.io/otel/metric => go.opentelemetry.io/otel/metric v1.38.0

replace go.opentelemetry.io/otel/trace => go.opentelemetry.io/otel/trace v1.38.0

replace golang.org/x/crypto => golang.org/x/crypto v0.36.0

replace go.opentelemetry.io/auto/sdk => go.opentelemetry.io/auto/sdk v1.1.0 // indirect

require (
	github.com/ONDC-Official/ondc-crypto-sdk-go v0.2.1
	github.com/andybalholm/brotli v1.2.2
	github.com/beckn-one/beckn-onix v1.3.0
	github.com/extedcouD/HttpRequestRemapper v0.0.2
	github.com/redis/go-redis/extra/redisotel/v9 v9.17.2
	github.com/redis/go-redis/v9 v9.17.2
	github.com/rickb777/date v1.22.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/spf13/viper v1.21.0
	github.com/stretchr/testify v1.11.1
	go.opentelemetry.io/otel v1.39.0
	go.opentelemetry.io/otel/metric v1.39.0
	golang.org/x/crypto v0.46.0
	google.golang.org/grpc v1.78.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
	validationpkg v0.0.0-00010101000000-000000000000
)

require (
	github.com/AsaiYusuke/jsonpath v1.6.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.14.2 // indirect
	github.com/bytedance/sonic/loader v0.4.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.9 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/matttproud/golang_protobuf_extensions/v2 v2.0.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_golang v1.18.0 // indirect
	github.com/prometheus/client_model v0.6.0 // indirect
	github.com/prometheus/common v0.45.0 // indirect
	github.com/prometheus/procfs v0.12.0 // indirect
	github.com/redis/go-redis/extra/rediscmd/v9 v9.17.2 // indirect
	github.com/rickb777/plural v1.4.7 // indirect
	github.com/rs/zerolog v1.34.0 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.46.0 // indirect
	go.opentelemetry.io/otel/sdk v1.38.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.38.0 // indirect
	go.opentelemetry.io/otel/trace v1.39.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/arch v0.0.0-20210923205945-b76863e36670 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/text v0.33.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251029180050-ab9386a59fda // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)
