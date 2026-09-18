package main

import (
	"sync"
	"testing"
)

func TestAttachState_AttachDetach(t *testing.T) {
	st := newAttachState()
	if _, ok := st.Current("telegram"); ok {
		t.Fatal("처음에는 붙은 것이 없어야 함")
	}
	st.Attach("telegram", SessionInfo{Host: "dev", Name: "proj-a-cf"})
	got, ok := st.Current("telegram")
	if !ok || got.Name != "proj-a-cf" {
		t.Fatalf("붙은 세션이 어긋남: %+v %v", got, ok)
	}
	if _, ok := st.Current("web:7"); ok {
		t.Error("다른 대화까지 붙으면 안 됨")
	}
	if !st.Detach("telegram") {
		t.Error("붙어 있었으면 Detach 가 true 여야 함")
	}
	if st.Detach("telegram") {
		t.Error("이미 풀린 것을 또 풀면 false 여야 함")
	}
}

func TestAttachState_RecallByNumber(t *testing.T) {
	st := newAttachState()
	st.Remember("telegram", []SessionInfo{
		{Name: "proj-a-cf", Addressable: true},
		{Name: "proj-b-4f", Addressable: true},
	})
	got, ok := st.Recall("telegram", 2)
	if !ok || got.Name != "proj-b-4f" {
		t.Fatalf("2번이 어긋남: %+v %v", got, ok)
	}
	if _, ok := st.Recall("telegram", 0); ok {
		t.Error("0번은 없어야 함")
	}
	if _, ok := st.Recall("telegram", 3); ok {
		t.Error("범위 밖은 없어야 함")
	}
	if _, ok := st.Recall("web:7", 1); ok {
		t.Error("목록을 본 적 없는 대화에서는 번호가 듣지 않아야 함")
	}
}

func TestAttachState_ConcurrentUse(t *testing.T) {
	st := newAttachState()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.Attach("telegram", SessionInfo{Name: "x"})
			st.Remember("telegram", []SessionInfo{{Name: "y"}})
			st.Current("telegram")
			st.Recall("telegram", 1)
			st.Detach("telegram")
		}()
	}
	wg.Wait() // -race 에서 걸리지 않으면 통과
}
