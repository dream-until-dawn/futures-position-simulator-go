# F9a：挂单顺序与存档设计复核

本稿以 main `6254e46` 为基线；当前执行状态与评审责任见 [state.md](./state.md)。仅设计，不实现 F9c 撮合或新增成交口径。

## 源码核验与边界

`order/freeze.go` 的 Book 只保存请求与冻结，Live 按 ID 字典序输出；`state.go` 按 Live 导出并逐笔 Insert 恢复。`place.go` 的 Fill 在失败时通过 Insert 放回委托。直接给 Insert 加自增序号会导致失败回滚改变先后顺序。`settle.go` 要求无挂单才能结算，但不应因此重置历史序号。

建议保留现有 Live 的字典序语义，新增有明确语义的 OrderedIDs 给未来撮合使用，避免调用方、既有存档排序及测试无意改变。挂单顺序仅表示本模拟器接收成功的顺序，不能冒充柜台原始排队顺序；PlaceAccepted 同样按本地接收顺序编号。

## 数据与接口

Book 增加私有 `lastSequence uint64`，entry 增加 `sequence uint64`。首次成功 Insert 分配 1；失败不递增；Remove 不回收编号。撤单或成交后复用 ID 属于新委托，获得新编号。超过 uint64 上限时拒绝新单，禁止回绕；原有撤单和成交仍可执行。

保留 Insert / Get / Remove / Live 的现有签名。拟在 order 目录新增 sequence.go 与 sequence_test.go，承载序号实现与测试（文件尚未创建）。拟增加 `Sequence(id) (uint64, bool)`、`LastSequence() uint64`、`OrderedIDs() []string`。

恢复路径拟使用 `order.RestoreBook(lastSequence uint64, entries []BookEntry) (*Book, error)`，其中 BookEntry 包含 ID、Request、Frozen、Sequence。构建新簿，拒绝重复 ID、零序号、重复序号、超过高水位的序号及不合法冻结；不得改变调用方数据。校验冻结沿用已有 FreezeOf 重算逻辑，门面保留账户和持仓合计校验。

回滚使用独立的 `Reinsert(id, req, in, sequence) error`，只允许 1 至当前高水位内未占用的序号，拒绝 ID 或序号冲突，不改变高水位。它只供已知原序号的回滚使用；一般新单仍必须 Insert。该接口不能证明外部调用者提供的历史真实，导出注释明确此边界，不能用于普通挂单抢优先级。

## 存档与事务

State 新增 `LastOrderSequence uint64`；OrderState 新增 `Sequence uint64`。StateFormat 从 4 升到 5；若实施前有其他格式变更，重新确认基线后使用下一个版本号。所有旧格式统一拒绝，包括空挂单存档，不迁移、不按 ID 推断编号。

Orders 继续按 ID 稳定导出；Restore 以显式 Sequence 重建顺序，不信任数组排列。允许撤单形成的序号空洞、空簿携带非零高水位；高水位 0 只允许空簿。高水位等于 uint64 最大值可恢复，但不再允许新单。

Place / PlaceAccepted 成功后才改变序号；Insert 失败时已有账户解冻回滚仍生效。Fill 删除前保存原序号，两条失败路径均用 Reinsert 放回；账户、持仓、费用、额度、挂单和高水位必须与调用前完整相等。放回失败沿用模拟器失效机制。Submit 不入簿，不消耗序号。风险 103 的有效请求转换保持不变。

## 验收与破坏验证

| 场景 | 必须检出的错误 |
|---|---|
| 依次挂 z、a、m；多合约交错 | OrderedIDs 为 z、a、m；Live 仍为 a、m、z，不能把字典序当时间顺序 |
| 重复 ID、空 ID、无效冻结、资金不足 | 完整 State 不变；下一次成功编号无跳增 |
| 撤掉中间单、撤完全部、复用 ID | 不回收编号；空簿导出恢复后继续递增 |
| Fill 的成交失败与解冻失败 | 原单回到原序号，其他单顺序和完整状态不变；无法正常公开触发的分支用包内故障注入 |
| 打乱存档数组后恢复 | 顺序由 Sequence 决定；两次 State 与继续挂单均可预测 |
| 零/重复/越界序号、重复 ID、旧格式空簿 | 精确拒绝，不能自动修补 |
| 最大序号及结算跨日 | 新单耗尽报错、撤单成交可用；结算不重置编号 |

实施时先写能在旧实现上失败的测试，再接线。破坏项覆盖忽略序号排序、失败先递增、回滚分配新号、恢复丢高水位、空簿重置、删除重复/越界守卫及格式退回 4。不要把预期红写成任意失败，必须匹配目标断言。根模块与 cmd/oracle 分别回归；本批改动相关破坏项通过后按项目既有完整清单验收，并报告预期绿项。

## 本次评审决策

请使用者确认：保留 Live 字典序并新增 OrderedIDs；采用全簿单调 uint64 高水位；增加上述恢复与回滚接口；格式升为 5 且不迁移。本稿通过后实施 F9a，完成后再次提交实现验收，合并主分支仍遵循使用者批准门禁。
