# futures-position-simulator-go

用 Go 实现**中国期货市场**的持仓与结算内核，力求贴近 100% 还原真实行为，
并保持良好的外部可引入性，供策略回测引擎等第三方使用。

一句话概括职责：**「成交与结算价 → 持仓 / 资金 / 结算单」的记账机。**

当前能力与验收看 [项目状态顶部](docs/state.md)，下方2026-09-09横幅保留当时的依据和边界。新增纯离线工作流入口见 [离线重放设计与操作](docs/offline-replay.md)：已发生的成交或显式手动成交 → 核算 → 跨日账单 → 完整存档，不从K线猜测成交。

**当前分支的离线能力**（准确验收、已合main的提交及仍在开发的批次见[state顶部](docs/state.md)，不以版本号推断API是否存在）：

| 能力 | 公开入口 | 边界 |
|---|---|---|
| 完整状态冻结适配 | `fixture.FrozenBookWithState` | 独立前置种子与完整顺序；失败不返回半簿 |
| 显式风控与补资信号 | `risk.Assess`、`Simulator.AssessRisk` | 阈值必填、补资向上到分、只读；不执行强平 |
| 保证金台阶/持仓期限 | `lifecycle.NewDailyRules`、`AssessLifecycle`、`SettleWithRules` | 资格、来源、覆盖及期限须明确给出；不内置未知市场制度 |
| 规则存取与跨日回放 | `DailyRules.Save`、`lifecycle.Load`、`replay.Payload.Lifecycle`、`Settlement.NextRules` | 原始基准和声明一起保存；旧日/版本恢复拒绝；状态与配置原子更新 |
| 回放风控/生命周期报告 | `Event.RiskPolicy`、`Frame.Risk`、`Frame.Lifecycle` | 有效前缀、独立重算；证据按规则绑定，不冒充柜台原始账单 |
| K线撮合、真实未收敛规则 | F9c及实际取证清单 | 仍需合格真实样本；不以合成测试补造 |

规则、适配和完整示例见[离线集成设计](docs/offline-integration.md)与[独立模块操作](examples/offline-replay/README.md)。统一验证运行三个Go模块及五项fuzz，命令见[当前开发手册](docs/development.md)。

**历史依据（2026-09-09）**：下方引用块保留当时的语料与判断；“一个口子”和相应覆盖描述是当日结论，当前证据等级、实际差异及未收敛项以state和规则表为准。

> ### 状态（2026-09-09）：核算内核已实现并对拍；**口子只有一个**
>
> ~~文档阶段，尚无实现~~ —— 那句横幅从 2026-09-07 挂到 2026-09-09，
> 而这期间核算内核落地、跨日对拍跑通、判别实验跑了十几轮。
> ⚠️ 这里**刻意不写包数**：那是一个会随下一个包落地而过期的计数，
> 而计数的单一来源是 [state.md](docs/state.md) 的 `packages_done`。
> ⚠️ 它是本仓库**最显眼**位置上的一条过期陈述，靠机械守卫抓出来的，
> 不是靠谁想起来。
>
> **已实现并逐字段对拍**：持仓（含逐笔明细与今昨仓）、账户、手续费、保证金、
> 两套盈亏口径、日终结算、报单校验与冻结。进度见 [版本排期](docs/roadmap.md)。
>
> ⚠️ 报单校验与冻结（`order`）与 `oracle conformance` 的实时对拍**已于 2026-09-09
> 合入 `main`**（评审「有条件通过」，必修的泄漏面已修）。
>
> ⚠️ 原先这里还有一句「`dev` 与 `main` 当前一致」。**删掉了，因为那种句子
> 只在写下的那一刻是真的** —— 它不是被某次疏忽写错的，是**每次 `dev` 前进一步
> 就自动再假一次**，而没有任何东西会因此报警。要判断当下的分支关系，
> 请直接 `git log --oneline main..dev`，那个答案永远是新鲜的。
> 哪些**包**只在 `dev` 上，见下方那句区分 —— **它有守卫钉着**
> （`TestDevOnlyClaimIsAccurate`），所以它和上面那句不是一回事。
> ⚠️ **但仍然没有任何 semver tag** —— 没有 tag 时 `go get` 拿 `main` 的最新提交，
> 所以这些**取得到**；取得到不等于做了承诺。分支现状见
> [适用边界与保真度](docs/fidelity.md) 的状态横幅。
>
> **⚠️ 而这里是读这份 README 最要紧的一句：**
> 本库返回的绝大多数数字，**目前只有一个口子（天勤快期模拟）的观测支撑**。
> 关于那个口子已经量到的事实有四十余条（[state.md](docs/state.md) 的 `kq_facts`），
> 而升格为「规则已实测」的只有**个位数** —— 那一档要求**第二个独立来源**，
> 且该来源必须能**分开候选**（对上一个数不算，见 `rules_measured` 的升格判据）。
> ⚠️ 确切的数在 [state.md](docs/state.md)，这里刻意不抄 —— 它会变。
>
> **⚠️ 而「有几条」比不上「覆盖了什么」——** 评审方 20260909 把这一层点破了：
> 现有那几条覆盖的是**可用资金与挂单冻结**这一类；
> **保证金本身、手续费的率与取整、两套平仓盈亏口径，仍然只有一个口子。**
> 其中一条测到的还是一处**已知差异**（快期用收盘价、本库按规则用结算价），
> ⚠️ 它增加的是**文档的可信度**，不是**数字的可信度** —— 两者很容易被读成一件事。
>
> 「一个口子上对得上」不等于「规则对了」，正如当初「通路通了」不等于「规则对了」。
> ⚠️ 因此**尚未打任何 semver tag**：tag 是对外承诺，`go get` 会认它。
>
> 「近乎 100% 模拟」的确切含义、成立条件与已知边界，见
> [适用边界与保真度](docs/fidelity.md)；**出问题时不报错的那些**单列在
> [静默风险清单](docs/silent-risks.md)。

---

## 它和加密货币衍生品模拟器差在哪

差在**结算**。这一条决定了本项目不能照搬
[`okx-position-simulator-go`](https://github.com/dream-until-dawn/okx-position-simulator-go)
的模型，只能沿用它的方法论。

| | OKX（加密永续） | 中国期货 |
|---|---|---|
| 记账节奏 | **连续**：标记价一变，盈亏与保证金立刻变 | **逐日盯市**：日内一套账，日终按结算价兑现并重算 |
| 成本基线 | 开仓均价，一直不变 | 昨仓以**昨结算价**为基线，每日结算重置一次 |
| 盈亏口径 | 一套 | **两套并存**（逐日盯市 / 逐笔对冲） |
| 持仓身份 | 只有多空 | 多空 × **今昨** × 投机套保 |
| 保证金 | 一层，随标记价连续变动 | **两层**（交易所 / 期货公司），盘中很可能是静态的 |
| 强平 | 交易所的确定性算法，可 100% 复现 | **期货公司的风控制度，含人工环节，不可复现** |

⚠️ **最后一行决定了「100% 模拟」的边界。** 本库**只建模强平判据，不建模强平执行**
——在中国期货语境下声称能 100% 还原爆仓过程是不诚实的。理由见
[fidelity.md](docs/fidelity.md) 第 3 节。

---

## 文档

| 文档 | 内容 |
|---|---|
| [架构设计与决策记录](docs/design.md) | ADR、包结构、双层时钟的 API 形态、验收标准 |
| [中国期货规则调研](docs/cn-futures-rules.md) | 逐条规则 + 证据等级 + **待实测项的判别实验** |
| [适用边界与保真度](docs/fidelity.md) | 什么条件下成立、验到什么程度、哪些是取舍 |
| [静默风险清单](docs/silent-risks.md) | **出错时不报错**的那些，及各自的守卫 |
| [版本排期](docs/roadmap.md) | 版本计划、分支约定、覆盖缺口 |
| [前期探针报告](docs/probes.md) | 数据源与对拍通路的**可复现**验证记录 |
| [CTP / SimNow 侧对拍的设计](docs/ctp-oracle.md) | ⚠️ **实现之前**写的：依赖决定、分期、以及「声明 ≠ 行为」这条组织原则 |

**建议的阅读顺序**：本页 → [probes.md](docs/probes.md)（事实基础）→
[cn-futures-rules.md](docs/cn-futures-rules.md)（规则）→ [design.md](docs/design.md)（取舍）。

---

## 覆盖范围（v1.0 目标）

| 项 | 覆盖 |
|---|---|
| 交易所 | 上期所 / 上期能源 / 大商所 / 郑商所 / 中金所 / 广期所 |
| 品种类别 | **仅期货**（期权、组合明确排除） |
| 投机套保 | **仅投机**（字段保留，逻辑留待后续） |
| 今昨仓 | 区分与不区分两条链路，按合约的规则数据决定 |
| 币种 | 仅 CNY |

覆盖的核算：开平仓与今昨仓管理、**两套盈亏口径**、两层保证金、单向大边、
开/平昨/平今三档手续费、报单校验与冻结、内置撮合、**逐日盯市日终结算与结算单**、
风险度与追保判据、交割月保证金递增。

---

## 验收：双口子逐字段对拍

「100% 模拟」若不能被自动验证就只是一句口号。

> **逐字段比对，每个字段落进三档：一致（且本次样本内被触发过）/ 已声明不建模
> （且带到期版本）/ 对上但未触发。三档之外判失败，第三档单独计数且不算通过。**

第三档堵的是**零值假通过**——字段两边都是零、样本从未触发它，对拍照样判「一致」。

| 口子 | 协议 | 角色 |
|---|---|---|
| 天勤快期模拟 | JSON over WebSocket（DIFF，RFC 7386 merge patch） | **主对拍**：账户与持仓的**全部**字段 + 报单成交（数量见 [state.md](docs/state.md)）。纯 Go 可达，无需 cgo（**已实测跑通**） |
| SimNow（CTP） | CTP API（Windows 走 `syscall`，Linux 走 cgo） | **权威裁决**：两套平仓盈亏口径、完整费率表、以及测量天勤与真实柜台的口径差 |

再加一条中国期货特有的、也是最有力的：**结算单对拍**。
结算单是逐日盯市的最终陈述，期初结存、当日盈亏、手续费、保证金占用、风险度
在一张表上互相勾稽。

⚠️ **「与快期模拟一致」不等于「与真实账户一致」。**
这个差目前完全未测量，本库的文档会一直如实措辞。

---

## 开发

日常操作从 [当前开发与离线验证手册](docs/development.md) 开始；当前进度只看 [state.md](docs/state.md) 顶部最新批次，历史日期段不代表现在仍待实施。

纯离线演示（需先安装并缓存Go依赖；二进制运行不联网、不读柜台凭据；输出必须是新文件）：

```text
go run ./cmd/replay demo -out demo-bundle.json
go run ./cmd/replay run -bundle demo-bundle.json -allow-placeholder -out demo-report.json
go run ./cmd/replay diff -left demo-report.json -right demo-report.json -out demo-diff.json
```

demo只生成完全合成的placeholder规格与合成日历，包含自然周末也不代表真实交易日；省略-allow-placeholder会明确失败。二进制diff退出0表示相同、2表示有差异、1表示输入/执行/落盘失败（go run自身可能把子程序非零码映射成1，应查看其exit status或直接运行编译后的程序）。独立模块接入示例在 [examples/offline-replay](examples/offline-replay/main.go)，可进入该目录执行go run .及go test .；生命周期/风控与完整状态冻结示例分别用 `go run . -scenario lifecycle`、`go run . -scenario frozen`，负例和手算见该目录README。

挂单序号与存档示例见 [sequence_example_test.go](order/sequence_example_test.go)，可运行 `go test ./order -run Example -count=1`。`Live` 按 ID 排序；`OrderedIDs` 表示本地成功接收顺序。低层 Book 不执行完整报单校验；一般业务应通过 Simulator 的 Place / PlaceAccepted 接入，并用 State / Restore 保存完整账户。当前 StateFormat 为 5，旧格式明确拒绝且不自动迁移。K线撮合 F9c 仍待真实探针证据，当前 Advance 只守卫并计价。

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

2026-09-07首次探针记录过本机 `proxy.golang.org` 不可达（[probes.md](docs/probes.md) §1）；这是历史网络观测，不代表当前仍不可达。上面的代理命令保留当时配置示例，不作为通用推荐或离线运行前提；纯离线验证需要预先缓存依赖，本批不修改全局代理配置。

对拍与探针需要柜台凭据：

```bash
cp .env.example .env
```

[`.env.example`](.env.example) 里逐项说明了每个值从哪里取。`.env` 已在 `.gitignore`。
**主库 `futsim` 不读任何环境变量**——它是纯计算库，不联网；凭据只被
`cmd/oracle`（独立嵌套模块，含 `probe` 与 `conformance` 两个子命令）使用。

分支约定见 [roadmap.md](docs/roadmap.md)：`dev` 是开发分支，`main` 只接受来自 `dev`
的合并，在版本验收达成时才打 tag。

---

## 许可

[MIT](LICENSE)
