package refdata

import (
	"reflect"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestFirstOpenConsumerGate保留可在旧源码编译的兼容探针：旧消费者仅查SessionAt，
// 新接口存在后改为精确比较首开。正式算法另由直接调用的正反向测试约束。
func TestFirstOpenConsumerGate(t *testing.T) {
	c := testCalendar(t)
	late := at(t, "2026-09-08 10:00:00")
	accepted := false
	method := reflect.ValueOf(c).MethodByName("FirstOpen")
	if method.IsValid() {
		result := method.Call([]reflect.Value{reflect.ValueOf(types.TradingDay(20260908)), reflect.ValueOf(types.SHFE), reflect.ValueOf("rb")})
		if result[1].IsNil() {
			accepted = result[0].Interface().(time.Time).Equal(late)
		}
	} else {
		_, err := c.SessionAt(late, types.SHFE, "rb")
		accepted = err == nil
	}
	if accepted {
		t.Fatal("时段内10点被当作交易日首开")
	}
}
