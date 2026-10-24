# 当前开发与离线验证手册

## 当前入口与边界

核算内核的当前实现与验收以 [state.md](./state.md) 顶部最新批次为准，历史日期段保留当时判断；路线与设计分别见 [roadmap.md](./roadmap.md)、[design.md](./design.md)。第一批五项见 [offline-work.md](./offline-work.md)，第二批设计与验收见 [offline-resilience.md](./offline-resilience.md)。挂单序号和K线守卫已实现，F9c撮合仍需合格挂单价探针证据；非交易日只补离线能力，不补造规则实测。

## 本地与CI命令

从仓库根运行，Python仅用标准库；Go按两个go.mod的最低要求安装，本批实际测量为Go1.26.1。所有命令返回非零都表示未通过，日志默认进入系统临时目录，也可用--out显式指定。回归与竞态模式拒绝真实跳过和零测试；在已跟踪文件有改动时可做回归，但运行后内容必须与运行前一致。完整破坏扫描要求干净工作区，建议独立工作树。

```text
python -m unittest discover -s scripts -p "test_*.py"
python scripts/validate.py --suite regression
python scripts/validate.py --suite race
python scripts/validate.py --suite fuzz --fuzz-time 20s
python scripts/validate.py --suite mutation
python scripts/validate.py --suite bench
```

性能模式默认5个样本、每样本30次操作，可用--bench-count与--bench-time调整；旧的3次操作基线保留作历史记录。超时/中断可能在破坏施加与还原之间终止，工具不会把残留源码自动覆盖为HEAD；必须保留失败证据、核实差异后恢复，再从干净修订整轮重跑。

race要求可用的C编译器；本机中文工具链目录若触发gcc路径问题，可用仅本进程的ASCII路径与CC/库搜索路径，不能把编译失败或跳过记为竞态通过。CI分别在Windows2022和Ubuntu24.04执行两个模块的回归、竞态及限时模糊测试；完整破坏扫描仅workflow_dispatch显式选择full_mutation时运行，日志作为artifact保留。[工作流](../.github/workflows/offline.yml) 使用[setup-go官方参数](https://github.com/actions/setup-go)，Windows编译器环境依据[官方镜像清单](https://github.com/actions/runner-images/blob/main/images/windows/Windows2022-Readme.md)，最后仍以实际作业日志为准。非Windows的CTP仅有报错桩，CI不连接柜台、不读取.env。

## 覆盖与证据阅读

状态性质测试把JSON恢复后的后续行为与未恢复实例逐步比较，并用独立费用、持仓、挂单集合和序号账本兜底；随机输入最多128步，固定种子与明确的成功/失败路径随普通回归执行。FuzzStateSequence仅探索程序状态性质，不验证真实费率。

原38项预期绿逐项审计见 [审计记录](./validation/green-audit-20261024.json)。第一批关闭403与426，随后36项中27项补齐离线测试，当前保留9项（8项刻意演示及441真实种子核对）；合成样本不提升真实观测。审计守卫核对每项的唯一性、分类与清单状态；新增预期绿必须同步登记。完整扫描结果必须报告预期红、预期绿、异常和恢复情况，不用总数代替分类。

验证入口以 HEAD、索引与已跟踪文件实际字节核对运行前后；每工作树的 offline-results/.validation.lock 拒绝并行运行。异常退出遗留锁须先确认所有验证进程结束并检查源码还原，再人工移除；不能抢锁或拼接上轮结果。--timeout 指定每条命令墙钟秒数（默认3600），超时/中断终止进程树；Windows使用Job Object，Linux使用独立进程组。强制杀掉验证器或系统断电不保证执行清理，遗留 running 报告、锁或源码改动均不能验收。需要从干净修订重新完整运行。

完整破坏证据复核：`python scripts/verify_evidence.py --certificate 证书.json --report mutations.json --summary summary.json`。它从证书指向的 Git 提交读取清单，核对源码tree、三个SHA256、唯一性、逐项字段、断言片段、分类、退出码及运行前后内容/索引一致；这是证据一致性检查，不是签名或真实柜台证明。

## 下次实际探针登记检查表

以下是待填写模板，不构成下单许可，也不使旧工具重新可执行。2026-10-23窗口已失效。

- 实际交易日期、交易日历出处、模拟柜台对应窗口及启动/收尾截止；不把自然日直接当交易日。
- 柜台与前置、合约、乘数、tick、到期日、交易状态、费率结构；只记录白名单业务字段，不放凭据。
- 账户干扰、未终结委托、当日开仓累计及种子归属；顺序查询不当作原子快照。
- 候选、判据、无判别力及反例处理；最大手数、敞口、等待时间、允许的收尾范围与具体授权。
- 把新日期和实值写入方案及工具守卫，运行本地故障矩阵、回归与适用破坏扫描后，提交推送登记；重新干净构建，核对完整提交与构建标记。
- 仅在新登记窗口内实测，保留逐阶段报告；未知终态停止，不能扩大清理到其他任务。事后先核收尾，再离线重算证据。lh两手实验仍需具体手数和跨结算范围授权。

离线故障矩阵覆盖拒单、提交超时、撤单错误/途中成交、初始与成交后查询失败，以及全部八个落盘阶段失败。最终落盘失败也必须把返回报告标成Failed；确认本次一手时按归属收尾，状态未知时保留失败，不追加猜测性订单。

## 性能基线与复现

合成100/1000笔持仓及挂单的State、Restore、Fill、Settle三次样本、环境与源码哈希见 [性能记录](./validation/performance-20261024.json)。每个样本固定3次操作，Fill/Settle每次恢复相同起点且恢复不计时；样本小、噪声较大，不设跨机器硬阈值。State/Restore包含挂单，Settle不含挂单，不能直接把不同操作的数字解释为同一任务的优劣。

本机1000规模中位数约为State0.36ms、Restore15.96ms、Fill2.09ms、Settle3.22ms，全部包含Go十进制与内存分配成本。后续如需优化，先扩大采样并profile恢复路径，保留序号/冻结/事务语义的性质与破坏回归。原始完整样本保存在上述JSON中，不只保留最快的一次。
