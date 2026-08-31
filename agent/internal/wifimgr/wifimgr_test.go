package wifimgr

import (
	"encoding/json"
	"testing"
)

const iwScanSample = `BSS 02:00:00:00:00:01(on wlan0)
	last seen: 1234567 ms ago (boottime)
	freq: 2412
	beacon interval: 100 TUs
	signal: -45.00 dBm
	SSID: CMCC-fpeh
	RSN:	 * Version: 1
	WPA:	 * Version: 1
BSS 02:00:00:00:00:02(on wlan0)
	freq: 2437
	signal: -60.00 dBm
	SSID: TP-D304
	RSN:	 * Version: 1
BSS 02:00:00:00:00:03(on wlan0)
	freq: 2462
	signal: -70.00 dBm
	SSID: TP-D304
BSS 02:00:00:00:00:04(on wlan0)
	freq: 5180
	signal: -55.00 dBm
	SSID: Office-5G
	RSN:	 * Version: 1
BSS 02:00:00:00:00:05(on wlan0)
	freq: 5180
	signal: -40.00 dBm
	SSID: hidden-ssid
`

func TestParseIwScanAPs(t *testing.T) {
	aps := parseIwScanAPs([]byte(iwScanSample))
	if len(aps) != 5 {
		t.Fatalf("期望 5 个 AP，实际 %d: %+v", len(aps), aps)
	}
	if aps[0].SSID != "CMCC-fpeh" || aps[0].Channel != 1 || aps[0].Band != "2.4" {
		t.Errorf("AP[0] 解析错误: %+v", aps[0])
	}
	if aps[0].Security != "WPA2" {
		t.Errorf("AP[0] 安全应为 WPA2（RSN 优先），实际 %q", aps[0].Security)
	}
	if aps[3].Channel != 36 || aps[3].Band != "5" {
		t.Errorf("5G AP 解析错误: %+v", aps[3])
	}
	// hidden-ssid 也保留（前端可显示 *)
	if aps[4].SSID != "hidden-ssid" {
		t.Errorf("hidden SSID 应保留原名用于显示")
	}
}

func TestAggregateBandsScores(t *testing.T) {
	aps := parseIwScanAPs([]byte(iwScanSample))
	bands := aggregateBands(aps)
	b24 := bands["2.4"]
	if b24 == nil {
		t.Fatal("应有 2.4 频段")
	}
	if ch1 := b24.Channels[1]; ch1 == nil || ch1.Count != 1 || ch1.BestRSSI != -45 {
		t.Errorf("ch1 统计错误: %+v", ch1)
	}
	if ch6 := b24.Channels[6]; ch6 == nil || ch6.Count != 1 {
		t.Errorf("ch6 统计错误: %+v", ch6)
	}
	if ch11 := b24.Channels[11]; ch11 == nil || ch11.Count != 1 || ch11.Score == 0 {
		t.Errorf("ch11 统计或评分错误: %+v", ch11)
	}
	if b5 := bands["5"]; b5 == nil || b5.Channels[36].Count != 2 {
		t.Errorf("5G ch36 应有 2 个 AP: %+v", b5)
	}
	// 强信号信道得分应高于差信号信道
	if b24.Channels[1].Score <= b24.Channels[11].Score {
		t.Errorf("ch1(-45) 得分应高于 ch11(-70): ch1=%d ch11=%d",
			b24.Channels[1].Score, b24.Channels[11].Score)
	}
}

func TestChannelFreqRoundTrip(t *testing.T) {
	cases := map[int]int{1: 2412, 6: 2437, 11: 2462, 36: 5180, 149: 5745}
	for ch, freq := range cases {
		if got := channelOfFreq(freq); got != ch {
			t.Errorf("channelOfFreq(%d)=%d 期望 %d", freq, got, ch)
		}
		if got := freqOfChannel(ch); got != freq {
			t.Errorf("freqOfChannel(%d)=%d 期望 %d", ch, got, freq)
		}
	}
}

func TestParseNmcliAPs(t *testing.T) {
	out := "CMCC-fpeh:80:WPA2:6\nTP-D304:55:WPA2:11\n:40:WPA2:1\nother:100:WPA2:36\n"
	aps := parseNmcliAPs([]byte(out))
	if len(aps) != 3 {
		t.Fatalf("期望 3 个 AP（空 SSID 忽略），实际 %d", len(aps))
	}
	if aps[0].SSID != "CMCC-fpeh" || aps[0].Channel != 6 || aps[0].Band != "2.4" {
		t.Errorf("AP[0] 错误: %+v", aps[0])
	}
	if aps[2].Channel != 36 || aps[2].Band != "5" {
		t.Errorf("5G AP 错误: %+v", aps[2])
	}
	// signal 100 → rssi = 100/2-100 = -50
	if aps[2].RSSI != -50 {
		t.Errorf("RSSI 换算错误: %d", aps[2].RSSI)
	}
}

func TestDedupAPs(t *testing.T) {
	aps := []APInfo{
		{BSSID: "aa:aa:aa:aa:aa:01", SSID: "x", RSSI: -60},
		{BSSID: "aa:aa:aa:aa:aa:01", SSID: "x", RSSI: -50},
		{BSSID: "aa:aa:aa:aa:aa:02", SSID: "y", RSSI: -40},
	}
	out := dedupAPs(aps)
	if len(out) != 2 {
		t.Fatalf("期望去重后 2 个，实际 %d", len(out))
	}
	if out[0].RSSI != -50 {
		t.Errorf("应保留信号更强版本，实际 %+v", out[0])
	}
}

func TestAssessReportJSON(t *testing.T) {
	// 结构可序列化，前端消费
	rep := &AssessReport{
		Interface: "wlan0",
		Current:   &CurrentLink{SSID: "CMCC-fpeh", Channel: 6, RSSI: -45, Signal: 75},
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("空序列化")
	}
}

func TestChannelCongestion(t *testing.T) {
	cong, score := scoreChannel(8, -60)
	if cong < 80 {
		t.Errorf("8 个 AP 拥堵度应 >=80，实际 %d", cong)
	}
	if score > 30 {
		t.Errorf("密集信道得分应较低，实际 %d（cong=%d）", score, cong)
	}
	cong, score = scoreChannel(1, -40)
	if cong != 20 {
		t.Errorf("1 个 AP 拥堵度应为 20，实际 %d", cong)
	}
	if score < 40 {
		t.Errorf("空信道强信号得分应较高，实际 %d", score)
	}
}