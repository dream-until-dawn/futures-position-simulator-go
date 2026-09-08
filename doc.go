// Package futsim 用 Go 实现中国期货市场的持仓与结算内核。
//
// 一句话概括职责：「成交与结算价 → 持仓 / 资金 / 结算单」的记账机。
//
// # 当前状态
//
// v0.1.0 开发中。本包目前只有包声明与文档守卫，核算逻辑尚未落地——
// 按 docs/roadmap.md 的排期原则，**每个版本先跑判别实验，再写实现**，
// 而判别实验尚未收敛。进度以 docs/state.md 为唯一来源。
//
// # 它和加密货币衍生品差在哪
//
// 差在结算。加密永续是连续记账：标记价一变，盈亏与保证金立刻变。
// 中国期货是逐日盯市：日内一套账，日终按结算价把浮盈兑现、把保证金重算、
// 把今仓变成昨仓，次日在新基线上重新开始。
//
// 照搬连续模型，每一天收盘都会错一次，而且错得很像对的。
//
// # 文档
//
//	docs/design.md            架构设计与决策记录
//	docs/cn-futures-rules.md  规则调研，每条带双坐标证据等级
//	docs/fidelity.md          适用边界与保真度
//	docs/silent-risks.md      静默风险清单
//	docs/state.md             项目状态与计数的唯一来源
//	docs/roadmap.md           版本排期
//	docs/probes.md            前期探针报告
//
// # 依赖
//
// 硬性约束：主模块的依赖树只有 github.com/shopspring/decimal 一个。
// 联网的部分隔离在 refdata/live 子包与 cmd/oracle 嵌套模块里。
package futsim
