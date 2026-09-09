module github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle

go 1.23

require (
	gitee.com/haifengat/goctp v1.10.17
	github.com/coder/websocket v1.8.15
	github.com/dream-until-dawn/futures-position-simulator-go v0.0.0
	github.com/shopspring/decimal v1.4.0
)

replace github.com/dream-until-dawn/futures-position-simulator-go => ../..
