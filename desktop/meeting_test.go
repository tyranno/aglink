package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMeeting_WritesNotesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	v := &VoiceService{}
	path, err := v.MeetingStart()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.MeetingNote(65.4, " 안녕하세요 "); err != nil {
		t.Fatal(err)
	}
	if err := v.MeetingNote(130.0, "다음 안건으로 넘어가죠"); err != nil {
		t.Fatal(err)
	}
	out, err := v.MeetingStop()
	if err != nil {
		t.Fatal(err)
	}
	var res struct{ Path string }
	_ = json.Unmarshal([]byte(out), &res)
	if res.Path != path {
		t.Fatalf("stop = %s", out)
	}
	txt, _ := os.ReadFile(path)
	if !strings.Contains(string(txt), "[01:05] 안녕하세요\n") || !strings.Contains(string(txt), "[02:10] 다음 안건으로 넘어가죠\n") {
		t.Fatalf("notes = %q", txt)
	}
	if err := v.MeetingNote(1, "끝난 뒤"); err == nil {
		t.Fatal("note after stop should fail")
	}
}

func TestMeeting_ListReadDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	v := &VoiceService{}
	path, err := v.MeetingStart()
	if err != nil {
		t.Fatal(err)
	}
	_ = v.MeetingNote(3, "첫 번째 말")
	out, err := v.MeetingList()
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Path, Preview string
		Lines         int
		Live          bool
	}
	_ = json.Unmarshal([]byte(out), &list)
	if len(list) != 1 || list[0].Path != path || !list[0].Live || list[0].Lines != 1 || !strings.Contains(list[0].Preview, "첫 번째 말") {
		t.Fatalf("list = %s", out)
	}
	if err := v.MeetingDelete(path); err == nil {
		t.Fatal("deleting the live meeting should fail")
	}
	_, _ = v.MeetingStop()
	txt, err := v.MeetingRead(path)
	if err != nil || !strings.Contains(txt, "[00:03] 첫 번째 말") {
		t.Fatalf("read = %q, %v", txt, err)
	}
	if _, err := v.MeetingRead(home + "/other.txt"); err == nil {
		t.Fatal("reading outside the meetings folder should fail")
	}
	if err := v.MeetingDelete(path); err != nil {
		t.Fatal(err)
	}
	if out, _ := v.MeetingList(); out != "[]" {
		t.Fatalf("after delete = %s", out)
	}
}

func TestMeeting_SummaryTitle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	v := &VoiceService{}
	path, _ := v.MeetingStart()
	_ = v.MeetingNote(1, "장비 납기 이야기")
	_, _ = v.MeetingStop()
	md := "# 장비 납기 일정 조율\r\n**분류:** 업무 회의\r\n\r\n## 3줄 요약\r\n- 납기를 2주 미루기로 함\r\n- 둘째\r\n\r\n## 결정 사항\r\n- 없음\r\n"
	title, err := v.MeetingSaveSummary(path, md)
	if err != nil || title != "장비 납기 일정 조율" {
		t.Fatalf("save = %q, %v", title, err)
	}
	out, _ := v.MeetingList()
	var list []struct{ Title, Category, Gist, Summary string }
	_ = json.Unmarshal([]byte(out), &list)
	if len(list) != 1 || list[0].Title != "장비 납기 일정 조율" || list[0].Category != "업무 회의" || list[0].Gist != "납기를 2주 미루기로 함" || list[0].Summary == "" {
		t.Fatalf("list = %s", out)
	}
	if _, err := v.MeetingSaveSummary(home+"/x.txt", md); err == nil {
		t.Fatal("saving outside the meetings folder should fail")
	}
	_ = v.MeetingDelete(path)
	if _, err := os.Stat(list[0].Summary); !os.IsNotExist(err) {
		t.Fatal("the write-up should go with the meeting")
	}
}
