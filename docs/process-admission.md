# 进程准入身份留证与本轮查询树判别

## 1 事实、假设与范围

基线a6d09e241f63fe4226596e5d795c57a6b65666d0。2026-11-02 09:08唯一只读入口在process阶段拒绝，CIM成功、matching_count=1，没有.env读取、实际CTP子程序或柜台连接。原轮仅保存计数，无法恢复当时匹配进程身份；原始目录和冻结材料均保留，不重新启动。不能把后来的离线复现倒写成当时已留有身份。

待证假设：生产bounded_process先启动Python process_worker.py，再由它启动PowerShell查询。旧查询把入口路径拼进-Command，并对整段Python命令行做子串匹配，查询worker自身可能因此命中。先通过同一生产接线的纯离线CIM探针记录实际PID/父PID/创建时间/映像/命令行，复现业务拒绝；不执行已结束的只读封装、不读.env、不创建CTP API。

本批只修改非交易准入和测试，不改生产握手期限、报撤单、策略与安全阀。历史进程准入根因与历史结算确认超时分别记录，不能互相替代。

## 2 模块、身份与判别

新增scripts/readonly_admission/processes.py，负责固定快照采集、Windows命令行解析、严格结构/创建时间/父子链验证和私有判别记录。registered.py继续在所有规则及.env检查之前调用进程准入；runner只记录本次受监管命令的实际worker PID，不新增启动或重试。

查询脚本为固定常量，不插入入口路径。只采集Python/只读执行器候选和采集器本身，字段为PID、父PID、创建时间、映像路径、名称、命令行；不取用户/账户、环境变量或其他服务数据。完整原始输出和身份仅保存在本轮私有文件。公开阶段报告只给有限计数/摘要/失败分类。

仅本轮当前Python、已记录的查询worker以及其直接PowerShell采集器可视为本轮查询树。必须逐项核PID唯一、映像、明确argv、父子关系及创建先后；不能忽略所有子孙进程、所有同名Python或全部包含process_worker.py的命令。PID会复用，父PID本身不足以证明归属。其他只读执行器、旧/新轮入口或无法排除的相关进程继续阻断；字段缺失、权限失败、畸形/重复记录、身份不一致均拒绝。

Windows参数用CommandLineToArgvW解析并正确LocalFree，不用空格切分或POSIX shlex猜测。非Windows不提供真实CIM执行，仍可运行合成判别正反例。

首个真实新探针正确拒绝了主映像不一致：本机Python安装管理器使当前OS映像为bin/python.exe，而sys.executable指向pythoncore运行时。修订为GetModuleFileNameW读取当前实际映像，worker仍独立绑定sys.executable；不能为了兼容shim放开两者任意同名。该差异另有合成正例/伪装worker反例；原失败快照保留。

## 3 证据与失败原则

开始记录先于进程查询；查询原始字节经现有有界命令留证。身份评估在准入决定前排他写入、fsync及读回，缺失/失败不继续。记录包括已识别查询树、阻断者/未知项及创建时间、命令行/映像摘要和指向私有原始输出的绑定。不得把空列表、查询退出0或仅计数0等同于已核完整身份。

核验只作单次快照，不承诺原子枚举整个系统或阻止其他用户在快照后另启进程；本轮目录独占与共享锁继续负责本轮唯一性。无法证明相关身份就保守停止，不用额外CIM重查把失败覆盖成通过。不自动终止他人进程，不重连柜台。底层原始异常仍使用既有首错机制，不以留证失败洗掉原业务失败。

## 4 正反向与破坏验证

先红要求：旧生产查询在纯离线真实进程树上复现worker自匹配，身份能由记录证明；保留匹配脚本、原始记录和真实断言。不以编译/导入失败代替红。

正向：带空格/中文的Windows argv、完整本轮三进程链、无其他相关进程、无关Python、正常独占及全生产登记接线。反向：另一个入口/执行器、伪装worker、错误父PID/创建先后/映像/参数、重复PID、缺字段、解析失败、非成功查询、原始输出/身份落盘失败、查询后越窗、第二次启动。全部核后继.env与实际CLI为0。

真实Windows测试只启动本地无网络Python/PowerShell，核本轮查询树不被误报；Linux执行合成解析/编排及不支持平台拒绝，不能冒称Linux实跑CIM。新增Go边界入口将Python业务反例纳入完整破坏清单。原1098项不删减，旧锚点如需迁移须保持name/test/want/expect并独立证明同一性质；新增故障包括遗漏身份、放宽本轮树、忽略阻断者、字段不全当空及身份写失败继续。

准确新源码在干净副本完成全清单、定向、三模块严格regression/race、五个20秒fuzz、全部Python、Windows/Linux CI及独立逐项want/内容索引还原/引用哈希验真，再经dev合main。失败轮完整保留不拼接。只在发布、实际入口另行正反向/破坏资格验证与真实就绪落盘均完成后登记新的单次只读日期；不沿用11月2日上午窗口或直接连接。

## 5 依据与后续更新

Windows进程身份与父PID复用限制见[Win32_Process官方定义](https://learn.microsoft.com/en-us/windows/win32/cimwin32prov/win32-process)；命令行解析/内存释放见[CommandLineToArgvW官方定义](https://learn.microsoft.com/en-us/windows/win32/api/shellapi/nf-shellapi-commandlinetoargvw)。这些API定义不是本项目实际判别正确的证据，仍需上述真实与合成测试。

本页先登记设计与待证假设。复现、准确源码、计数、证书、发布和新窗口仅补真实已完成结果，不提前声称今天的身份已还原或现场握手已成功。

已完成离线旧接线复现：唯一命中为本轮受监管查询worker，PID/父PID/原始命令及摘要均有私有原始证据，未读.env或连接柜台。此结果证明可复现机制，不恢复上午缺失的历史身份。实际OS映像定义补充见[GetModuleFileNameW](https://learn.microsoft.com/en-us/windows/win32/api/libloaderapi/nf-libloaderapi-getmodulefilenamew)。
