module srs-learn/auth

go 1.22

require srs-learn/shared v0.0.0

require github.com/gin-gonic/gin v1.10.0

require (
	github.com/mattn/go-isatty v0.0.20 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace srs-learn/shared => ../shared
