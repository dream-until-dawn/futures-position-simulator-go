# 独立 Go 模块离线接入

本目录有自己的go.mod；只使用本库导出API。所有价格、费率、期限和资格均为合成示例，证据等级为placeholder，代码显式签字允许；没有柜台连接或报撤单。

在本目录运行：

```powershell
go test ./... -count=1
go run . -scenario replay
go run . -scenario lifecycle
go run . -scenario frozen
```

replay为原有两日25事件示例，最终结存1000000。lifecycle按100开一手，开仓费1，110结算兑现100，次日结存1099；公司保证金为110×10×(.2+.03)=253。显式风险上限.2时最小补资信号为166，代码不会自动入金。完整生命周期快照与State一起恢复，规则版本为3。

frozen使用独立前置状态（两手今多、无挂单/冻结、序号高水位17），按明确顺序冻结两笔各一手平今；手续费各3，成功簿高水位19。没有用账户/持仓输出反推种子。

以下两条故意以非零退出，分别展示缺下一日日规则、第三笔累计超平，错误不返回半个成功状态/委托簿：

```powershell
go run . -scenario lifecycle -invalid
go run . -scenario frozen -invalid
```

持久化使用lifecycle.DailyRules.Save/Load，初始回放包选择Payload.Lifecycle，Settlement.NextRules带完整下一日规则；这两个位置都不能只塞普通refdata快照。根scripts/validate.py的regression/race已包含本模块，不再仅依赖根go test ./...。
