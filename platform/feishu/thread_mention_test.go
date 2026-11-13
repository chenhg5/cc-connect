package feishu

import "testing"

// Mention requirement is scoped to the thread: once a thread is registered,
// any message inside it is accepted without an @.
func TestThreadMentionScope(t *testing.T) {
	p := &Platform{}
	if p.threadAllowed("t1") {
		t.Fatal("unknown thread must require a mention")
	}
	p.markThread("t1")
	if !p.threadAllowed("t1") {
		t.Fatal("registered thread must skip the mention requirement")
	}
	if p.threadAllowed("t2") {
		t.Fatal("other threads must still require a mention")
	}
	if p.threadAllowed("") {
		t.Fatal("empty thread id must not be allowed")
	}
}
