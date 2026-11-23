package security

import (
	"fmt"
	"sync"
)

// cappedBuffer 同时保留输出头部和尾部，中间丢弃并计数。
// 目的是让工具输出既能看清命令开头，也能看到失败时的末尾报错。
type cappedBuffer struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	headMax int
	tailMax int
	total   int
}

func newCappedBuffer(headMax, tailMax int) *cappedBuffer {
	return &cappedBuffer{headMax: headMax, tailMax: tailMax}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := len(p)
	b.total += n

	if len(b.head) < b.headMax {
		take := b.headMax - len(b.head)
		if take > len(p) {
			take = len(p)
		}
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if len(b.tail) > b.tailMax {
			b.tail = b.tail[len(b.tail)-b.tailMax:]
		}
	}
	return n, nil
}

// Result 返回文本与是否发生截断。
func (b *cappedBuffer) Result() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	dropped := b.total - len(b.head) - len(b.tail)
	if dropped <= 0 {
		return string(b.head) + string(b.tail), false
	}
	out := make([]byte, 0, len(b.head)+len(b.tail)+64)
	out = append(out, b.head...)
	out = append(out, []byte(fmt.Sprintf("\n… [输出截断，中间丢弃 %d 字节] …\n", dropped))...)
	out = append(out, b.tail...)
	return string(out), true
}
