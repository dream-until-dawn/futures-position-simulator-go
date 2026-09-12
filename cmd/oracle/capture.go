package main

import (
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// captureWithQuote 拍一份**带行情**的截面：补不上行情就一份都不返回。
//
// ⚠️ 它被抽出来是因为 20260911 夜盘写第二个会落盘的命令（`ctp-slices`）时，
// 我把「补行情失败 ⇒ 整份不落盘」那段**又抄了一遍** ——
// 而 `TestAttachQuoteHasOneCallSite` 当场红了，理由正是它注释里写的：
// **第二条调用路径会绕开那两条断言。**
//
//	⚠️ 那条守卫防的不是「有人写错」，是「有人写对了，但写在了守卫看不见的地方」。
//	两份各自正确的实现，与一份正确 + 一份漏了 return，
//	**在守卫红之前长得一模一样**。
//
// ⇒ 现在这条不变式只有一个实现，两个命令都从这里过。
// `symbol` 为空表示这次本来就不要行情 —— 那是正常情况，不是失败。
//
// ⚠️ **失败这一支必须 `return nil, …`，不许 `return fx, err`**：
// 后者把一份半截的截面交到调用方手上，而调用方**可能只看 err 不看 fx**，
// 也可能反过来。守卫 `TestQuoteFailureBlocksTheWrite`。
func captureWithQuote(c *ctp.Client, timeout time.Duration, note, symbol string) (*ctp.Fixture, error) {
	fx, err := c.Capture(timeout, note)
	if err != nil {
		return nil, err
	}
	if symbol != "" {
		if err := c.AttachQuote(fx, symbol, timeout); err != nil {
			return nil, fmt.Errorf("⚠️ 行情没补上，**整份截面不落盘**："+
				"一份缺了 quotes 的夹具与一份本来就不带 quotes 的分不开：%w", err)
		}
	}
	return fx, nil
}
