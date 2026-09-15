package futsim

import (
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// Place 挂一笔单：与 Submit 同一套校验，通过就冻结（账户侧保证金 + 手续费，持仓侧可平量）并记进挂单簿，**不成交**。
//
// 给自己撮合的引擎用：何时成交由引擎决定（Fill），撤单用 Cancel。被拒 / 没查成与 Submit 返回同样的错误，状态不动。
func (s *Simulator) Place(day types.TradingDay, at time.Time, id string, req order.Request) (order.Frozen, error) {
	if err := s.usable(day); err != nil {
		return order.Frozen{}, err
	}
	if _, _, dup := s.book.Get(id); dup {
		return order.Frozen{}, fmt.Errorf("委托 %s 已经在簿上", id)
	}
	fr, _, err := s.validate(day, at, req)
	if err != nil {
		return order.Frozen{}, err
	}
	if err := s.acc.Freeze(day, fr.Margin, fr.Commission); err != nil {
		return order.Frozen{}, err
	}
	if err := s.book.Insert(id, req, freezeInputOf(fr)); err != nil {
		if uerr := s.acc.Unfreeze(day, fr.Margin, fr.Commission); uerr != nil {
			s.broken = uerr
		}
		return order.Frozen{}, err
	}
	return fr, nil
}

// Cancel 撤一笔挂单，释放它冻住的东西。不在簿上报错。
func (s *Simulator) Cancel(day types.TradingDay, id string) error {
	if err := s.usable(day); err != nil {
		return err
	}
	_, fr, ok := s.book.Get(id)
	if !ok {
		return fmt.Errorf("委托 %s 不在簿上", id)
	}
	if err := s.acc.Unfreeze(day, fr.Margin, fr.Commission); err != nil {
		return err
	}
	if _, err := s.book.Remove(id); err != nil {
		s.broken = err
		return fmt.Errorf("⚠️ 已解冻而挂单簿删不掉，模拟器失效：%w", err)
	}
	return nil
}

// Fill 让一笔挂单**全量**成交，价 = 挂单价（match 的裁决：不做盘口、100% 全量；引擎决定的只是何时）。
//
// 先把挂单拿下、解冻，再 ApplyTrade；ApplyTrade 失败（例如 §13 #21 分歧段）⇒ 挂单与冻结原样放回。
func (s *Simulator) Fill(day types.TradingDay, id string) (match.Trade, error) {
	if err := s.usable(day); err != nil {
		return match.Trade{}, err
	}
	req, fr, ok := s.book.Get(id)
	if !ok {
		return match.Trade{}, fmt.Errorf("委托 %s 不在簿上", id)
	}
	tr := match.Trade{Instrument: req.Instrument, Direction: req.Direction, Offset: req.Offset,
		Hedge: req.Hedge, Price: req.Price, Volume: req.Volume}

	if _, err := s.book.Remove(id); err != nil {
		return match.Trade{}, err
	}
	if err := s.acc.Unfreeze(day, fr.Margin, fr.Commission); err != nil {
		if ierr := s.book.Insert(id, req, freezeInputOf(fr)); ierr != nil {
			s.broken = ierr
		}
		return match.Trade{}, err
	}
	if err := s.ApplyTrade(day, tr); err != nil {
		// 放回：解冻与删簿的逆操作；放不回则失效
		if ferr := s.acc.Freeze(day, fr.Margin, fr.Commission); ferr != nil {
			s.broken = ferr
		} else if ierr := s.book.Insert(id, req, freezeInputOf(fr)); ierr != nil {
			s.broken = ierr
		}
		return match.Trade{}, err
	}
	return tr, nil
}

// Live 是挂单簿上的委托编号，排好序。
func (s *Simulator) Live() []string { return s.book.Live() }

// freezeInputOf 从一笔冻结还原出能让 Book.Insert 重算出同一份冻结的输入。
func freezeInputOf(fr order.Frozen) order.FreezeInput {
	return order.FreezeInput{Margin: fr.Margin, Commission: fr.Commission,
		UndatedToday: fr.VolumeToday, UndatedHistory: fr.VolumeHistory}
}
