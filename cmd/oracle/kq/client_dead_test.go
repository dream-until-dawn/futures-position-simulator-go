package kq

import (
	"errors"
	"strings"
	"testing"
)

// TestDeadErrDistinguishesWhichStream 断言两条流的死活是**分开**报的。
//
// ⚠️ 起因是一次真实的漏测：连接断了，读循环打印一行日志就退出，
// 而没有任何人被告知 —— 上层盯盘继续按秒采样，采到的永远是最后那份截面。
// 「连接死了」与「什么都没变」在日志上长得一模一样，
// 而结算恰好发生在那段时间里（probes.md §13.4）。
//
// ⚠️ 两条流要分开报，因为后果不同：
// 交易流断了，账户与持仓从此不再更新 —— 那是致命的；
// 行情流断了，只影响行情字段。合成一句「连接断了」会让人分不清严重程度。
func TestDeadErrDistinguishesWhichStream(t *testing.T) {
	td := errors.New("交易流的错")
	md := errors.New("行情流的错")
	cases := []struct {
		name       string
		td, md     error
		wantNil    bool
		wantHas    []string
		wantNotHas []string
	}{
		{"都活着", nil, nil, true, nil, nil},
		{"只有交易流断", td, nil, false,
			[]string{"交易流断了", "账户与持仓截面从此不再更新"}, []string{"行情流断了"}},
		{"只有行情流断", nil, md, false,
			[]string{"行情流断了"}, []string{"账户与持仓"}},
		{"两条都断", td, md, false,
			[]string{"两条流都断了", "交易流的错", "行情流的错"}, nil},
	}
	if len(cases) != 4 {
		t.Fatalf("用例 %d 条，应为 4", len(cases))
	}
	for _, c := range cases {
		cli := &Client{deadTd: c.td, deadMd: c.md}
		err := cli.DeadErr()
		if c.wantNil {
			if err != nil {
				t.Errorf("⚠️ %s：应当返回 nil，得到 %v —— "+
					"一个总是报「断了」的检查，会训练人忽略它", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("⚠️ %s：应当报错却返回 nil —— "+
				"那样上层会继续采样，产出一段看起来平静的假证据", c.name)
			continue
		}
		for _, w := range c.wantHas {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s：错误信息里没有 %q：%v", c.name, w, err)
			}
		}
		for _, w := range c.wantNotHas {
			if strings.Contains(err.Error(), w) {
				t.Errorf("⚠️ %s：错误信息里不该有 %q —— "+
					"两条流的后果不同，混在一起会让人分不清严重程度：%v", c.name, w, err)
			}
		}
	}
	// 反向：Dead() 返回的就是塞进去的那两个，不做加工。
	cli := &Client{deadTd: td, deadMd: md}
	gotTd, gotMd := cli.Dead()
	if gotTd != td || gotMd != md {
		t.Errorf("Dead() 返回的不是原来那两个错误")
	}
}
