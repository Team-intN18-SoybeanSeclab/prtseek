package agent

import "testing"

func TestPersonalPersistentGoal(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{"持续解题 不要停止 直到获取flag", true},
		{"获取flag 不要停", true},
		{"CTF 场景：延时(3) 获取flag", true},
		{"Keep going until you get the flag", true},
		{"分析这道 CTF 题", false},
		{"继续", true},
		{"普通聊天", false},
	} {
		if got := PersonalPersistentGoal(tc.message); got != tc.want {
			t.Errorf("PersonalPersistentGoal(%q) = %v; want %v", tc.message, got, tc.want)
		}
	}
}
