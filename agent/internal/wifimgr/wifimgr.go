// Package wifimgr 边缘节点 WiFi 管理：信道扫描评估 + 备选网络自动切换。
//
// 设计原则（与 Agent 自治约束一致）：
//   - 评估逻辑在节点本地完成，控制面离线期间照常运行
//   - 配置持久化到 ConfigDir/wifi_guard.json（0600，含白名单密码）
//   - 自动切换默认关闭，由控制面下发开关；切换前做增益判定，切换后做连通性回退
//   - 底层数据源优先 iw（信息全），缺失时降级 nmcli
//
// 安全红线：只连接白名单内的 SSID（防钓鱼热点），所有切换留日志（内存窗口）。
package wifimgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 数据结构
// ---------------------------------------------------------------------------

// APInfo 单个扫描到的 AP。
type APInfo struct {
	BSSID    string `json:"bssid"`
	SSID     string `json:"ssid"`
	Freq     int    `json:"freq"`
	Channel  int    `json:"channel"`
	Band     string `json:"band"` // "2.4" / "5" / "6"
	RSSI     int    `json:"rssi"` // dBm（负值）
	Security string `json:"security"`
	Signal   int    `json:"signal"` // 0-100 归一化
}

// ChannelStat 单信道聚合统计。
type ChannelStat struct {
	Channel    int   `json:"channel"`
	Count      int   `json:"count"`
	BestRSSI   int   `json:"best_rssi"`
	AvgRSSI    int   `json:"avg_rssi"`
	Congestion int   `json:"congestion"` // 0-100 越高越拥挤
	Score      int   `json:"score"`      // -40..100 越高越好
}

// BandReport 一个频段内全部信道的统计。
type BandReport struct {
	Band     string                  `json:"band"`
	Channels map[int]*ChannelStat    `json:"channels"`
	Best     *ChannelStat            `json:"best,omitempty"`
}

// CurrentLink 当前 WiFi 链路状态（来自 iw dev link）。
type CurrentLink struct {
	SSID    string `json:"ssid"`
	BSSID   string `json:"bssid"`
	Freq    int    `json:"freq"`
	Channel int    `json:"channel"`
	Band    string `json:"band"`
	RSSI    int    `json:"rssi"`
	Signal  int    `json:"signal"`
	Bitrate string `json:"bitrate,omitempty"`
}

// Recommendation 评估建议。
type Recommendation struct {
	Kind    string `json:"kind"` // "ssid" | "channel"
	SSID    string `json:"ssid,omitempty"`
	Channel int    `json:"channel,omitempty"`
	Band    string `json:"band,omitempty"`
	RSSI    int    `json:"rssi,omitempty"`
	Reason  string `json:"reason"`
}

// AssessReport 一次扫描评估的完整报告（前端直接消费）。
type AssessReport struct {
	Interface       string            `json:"interface"`
	ScannedAt       string            `json:"scanned_at"`
	Tool            string            `json:"tool"` // "iw" | "nmcli"
	Current         *CurrentLink      `json:"current"`
	APList          []APInfo          `json:"ap_list"`
	Bands           map[string]*BandReport `json:"bands"`
	Recommendations []Recommendation  `json:"recommendations"`
	Guard           *GuardState       `json:"guard"`
}

// WhitelistEntry 白名单网络（SSID + 密码，密码仅写盘，不回显）。
type WhitelistEntry struct {
	SSID     string `json:"ssid"`
	Password string `json:"password,omitempty"`
}

// GuardConfig 自动切换引擎配置（wifi_guard.json）。
type GuardConfig struct {
	Enabled      bool             `json:"enabled"`
	Threshold    int              `json:"threshold"`      // 当前信号低于该值(dBm)触发评估，默认 -75
	MinMargin    int              `json:"min_margin"`     // 候选增益下限 dB，默认 8
	IntervalSec  int              `json:"interval_sec"`   // 探测间隔秒，默认 60
	CheckGateway string           `json:"check_gateway"`  // 切换后连通性检测目标，空则取默认路由网关
	Whitelist    []WhitelistEntry `json:"whitelist"`
	MaxLog       int              `json:"max_log"` // 保留的切换日志条数，默认 20
}

// SwitchLog 一次切换/检查事件。
type SwitchLog struct {
	Ts    string `json:"ts"`
	Event string `json:"event"` // switch / check / config
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	OK    bool   `json:"ok"`
	Msg   string `json:"msg,omitempty"`
}

// GuardState 供给前端展示的引擎状态（不含密码）。
type GuardState struct {
	Enabled      bool     `json:"enabled"`
	Threshold    int      `json:"threshold"`
	MinMargin    int      `json:"min_margin"`
	IntervalSec  int      `json:"interval_sec"`
	CheckGateway string   `json:"check_gateway"`
	Whitelist    []string `json:"whitelist"` // 仅 SSID
	LastCheck    string   `json:"last_check,omitempty"`
	LastSwitch   string   `json:"last_switch,omitempty"`
	Log          []SwitchLog `json:"log"`
}

// ---------------------------------------------------------------------------
// 常量与单例
// ---------------------------------------------------------------------------

const (
	DefaultThreshold    = -75
	DefaultMinMargin    = 8
	DefaultIntervalSec  = 60
	DefaultMaxLog       = 20
	nmProfilePrefix     = "ecp-"
)

// Manager 是 WiFi 管理引擎的单例载体。
type Manager struct {
	mu      sync.Mutex
	cfg     *GuardConfig
	log     []SwitchLog
	iface   string
	cfgPath string

	lastCheck time.Time
	lastScan  *AssessReport
	sudoOK    bool // 探测一次并缓存
}

var (
	_mgr     *Manager
	_mgrOnce sync.Once
)

// Configure 初始化单例（幂等）。cfgPath 为空时默认 ConfigDir/agent.yaml 同级。
func Configure(configDir, iface string) *Manager {
	_mgrOnce.Do(func() {
		if configDir == "" {
			configDir = "/etc/ecp"
		}
		if iface == "" {
			iface = "wlan0"
		}
		m := &Manager{
			cfgPath: filepath.Join(configDir, "wifi_guard.json"),
			iface:   iface,
		}
		m.sudoOK = sudoNopassOK()
		m.loadConfig()
		_mgr = m
	})
	return _mgr
}

// Default 返回已配置的单例；未配置时用默认路径兜底。
func Default() *Manager {
	if _mgr == nil {
		return Configure("", "")
	}
	return _mgr
}

// ---------------------------------------------------------------------------
// 配置读写（原子写盘 + 热加载）
// ---------------------------------------------------------------------------

func (m *Manager) loadConfig() {
	cfg := defaultConfig()
	data, err := os.ReadFile(m.cfgPath)
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	cfg.sanitize()
	m.cfg = cfg
}

func defaultConfig() *GuardConfig {
	return &GuardConfig{
		Threshold:   DefaultThreshold,
		MinMargin:   DefaultMinMargin,
		IntervalSec: DefaultIntervalSec,
		MaxLog:      DefaultMaxLog,
	}
}

func (c *GuardConfig) sanitize() {
	if c.Threshold == 0 {
		c.Threshold = DefaultThreshold
	}
	if c.Threshold > -50 {
		c.Threshold = DefaultThreshold
	}
	if c.MinMargin == 0 {
		c.MinMargin = DefaultMinMargin
	}
	if c.IntervalSec <= 0 {
		c.IntervalSec = DefaultIntervalSec
	}
	if c.MaxLog <= 0 {
		c.MaxLog = DefaultMaxLog
	}
	// 去重
	seen := map[string]bool{}
	out := c.Whitelist[:0]
	for _, w := range c.Whitelist {
		w.SSID = strings.TrimSpace(w.SSID)
		if w.SSID == "" || seen[w.SSID] {
			continue
		}
		seen[w.SSID] = true
		out = append(out, w)
	}
	c.Whitelist = out
}

// ApplyConfig 全量写入配置（校验 + 原子写 + 同步 nmcli profile）。
// 白名单项密码留空时沿用旧值（前端不回显密码，避免误清空）。
func (m *Manager) ApplyConfig(raw string) error {
	var c GuardConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return fmt.Errorf("配置 JSON 非法: %w", err)
	}
	c.sanitize()

	// 密码 merge：新配置密码为空且旧配置存在同 SSID → 沿用旧密码
	m.mu.Lock()
	if m.cfg != nil {
		oldBySSID := map[string]string{}
		for _, o := range m.cfg.Whitelist {
			oldBySSID[o.SSID] = o.Password
		}
		for i := range c.Whitelist {
			if c.Whitelist[i].Password == "" {
				if p, ok := oldBySSID[c.Whitelist[i].SSID]; ok {
					c.Whitelist[i].Password = p
				}
			}
		}
	}
	m.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.cfgPath), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp := m.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(mustJSON(c)), 0o600); err != nil {
		return fmt.Errorf("写临时配置失败: %w", err)
	}
	if err := os.Rename(tmp, m.cfgPath); err != nil {
		return fmt.Errorf("替换配置失败: %w", err)
	}

	m.mu.Lock()
	m.cfg = &c
	m.addLogLocked("config", "", "", true, "配置已更新 (enabled="+fmt.Sprint(c.Enabled)+")")
	m.mu.Unlock()

	// 同步 NetworkManager 连接 profile（白名单里有密码的）
	m.SyncProfiles()
	return nil
}

// Status 返回引擎状态（无密码）。
func (m *Manager) Status() *GuardState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		return &GuardState{}
	}
	st := &GuardState{
		Enabled:      m.cfg.Enabled,
		Threshold:    m.cfg.Threshold,
		MinMargin:    m.cfg.MinMargin,
		IntervalSec:  m.cfg.IntervalSec,
		CheckGateway: m.cfg.CheckGateway,
		Log:          append([]SwitchLog(nil), m.log...),
	}
	for _, w := range m.cfg.Whitelist {
		st.Whitelist = append(st.Whitelist, w.SSID)
	}
	if !m.lastCheck.IsZero() {
		st.LastCheck = m.lastCheck.Format(time.RFC3339)
	}
	for i := len(m.log) - 1; i >= 0; i-- {
		if m.log[i].Event == "switch" {
			st.LastSwitch = m.log[i].Ts + " " + m.log[i].To
			break
		}
	}
	return st
}

// ---------------------------------------------------------------------------
// 扫描与评估
// ---------------------------------------------------------------------------

// Assess 执行一次扫描并返回评估报告。
// iface 为空时使用单例默认接口。
func (m *Manager) Assess(iface string) (*AssessReport, error) {
	if iface == "" {
		iface = m.iface
	}
	rep := &AssessReport{
		Interface: iface,
		ScannedAt: time.Now().Format(time.RFC3339),
		Bands:     map[string]*BandReport{},
	}

	// 1) 扫描（优先 iw，降级 nmcli）
	aps, tool, err := m.scanAPs(iface)
	if err != nil {
		return nil, err
	}
	rep.Tool = tool
	// 去重：同一 BSSID 只保留信号最强一次
	rep.APList = dedupAPs(aps)

	// 2) 当前链路
	if link := m.currentLink(iface); link != nil {
		rep.Current = link
	}

	// 3) 信道聚合评估
	rep.Bands = aggregateBands(rep.APList)

	// 4) 推荐
	rep.Recommendations = m.recommend(rep)

	// 5) 引擎状态快照
	rep.Guard = m.Status()

	m.mu.Lock()
	m.lastScan = rep
	m.lastCheck = time.Now()
	m.mu.Unlock()
	return rep, nil
}

// scanAPs 扫描 AP 列表。返回 (AP列表, 工具名, 错误)。
func (m *Manager) scanAPs(iface string) ([]APInfo, string, error) {
	if toolExists("iw") {
		out, err := m.runPriv(25*time.Second, "iw", "dev", iface, "scan")
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			return parseIwScanAPs(out), "iw", nil
		}
		// iw 扫描需要 root 且可能被网卡驱动拒绝；失败继续降级
	}
	if toolExists("nmcli") {
		// NM 触发 rescan（需 sudo），等待一轮
		if m.sudoOK {
			_, _ = m.runPriv(10*time.Second, "nmcli", "dev", "wifi", "rescan", "ifname", iface)
			time.Sleep(3 * time.Second)
		}
		out, err := runTimeout(20*time.Second, "nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY,CHAN", "dev", "wifi", "list", "ifname", iface)
		if err != nil {
			return nil, "none", fmt.Errorf("iw 与 nmcli 均不可用/失败: %v", err)
		}
		return parseNmcliAPs(out), "nmcli", nil
	}
	return nil, "none", fmt.Errorf("缺少扫描工具：请安装 iw（apt install iw）")
}

// currentLink 读取当前连接（iw dev link 或 nmcli）。
func (m *Manager) currentLink(iface string) *CurrentLink {
	if toolExists("iw") {
		out, err := m.runPriv(10*time.Second, "iw", "dev", iface, "link")
		if err == nil {
			if lk := parseIwLink(out); lk != nil {
				return lk
			}
		}
	}
	if toolExists("nmcli") {
		out, _ := runTimeout(10*time.Second, "nmcli", "-t", "-f", "SSID,SIGNAL,CHAN,DEVICE", "dev", "wifi", "show")
		if lk := parseNmcliLink(out, iface); lk != nil {
			return lk
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 评估模型
// ---------------------------------------------------------------------------

// aggregateBands 按频段/信道聚合并评分。
func aggregateBands(aps []APInfo) map[string]*BandReport {
	bands := map[string]*BandReport{}
	for _, ap := range aps {
		band := ap.Band
		if band == "" {
			continue
		}
		br := bands[band]
		if br == nil {
			br = &BandReport{Band: band, Channels: map[int]*ChannelStat{}}
			bands[band] = br
		}
		cs := br.Channels[ap.Channel]
		if cs == nil {
			// BestRSSI 用 -200 起算：信号是负 dBm，0 起算会破坏 max 比较
			cs = &ChannelStat{Channel: ap.Channel, BestRSSI: -200}
			br.Channels[ap.Channel] = cs
		}
		cs.Count++
		if ap.RSSI > cs.BestRSSI {
			cs.BestRSSI = ap.RSSI
		}
		cs.AvgRSSI = (cs.AvgRSSI*(cs.Count-1) + ap.RSSI) / cs.Count
	}
	for _, br := range bands {
		for ch, cs := range br.Channels {
			cs.Congestion, cs.Score = scoreChannel(cs.Count, cs.BestRSSI)
			br.Channels[ch] = cs
		}
		// 标记最优信道
		var best *ChannelStat
		for _, cs := range br.Channels {
			if best == nil || cs.Score > best.Score {
				cp := *cs
				best = &cp
			}
		}
		br.Best = best
	}
	return bands
}

// scoreChannel 信道评分：拥堵度 0-100（AP 越多越挤），强度按最强信号归一，
// 综合分 = 强度×0.6 - 拥堵×0.4（范围约 -40..100）。
func scoreChannel(count, bestRSSI int) (congestion, score int) {
	congestion = count * 20
	if congestion > 100 {
		congestion = 100
	}
	strength := (bestRSSI + 90) * 100 / 40
	if strength < 0 {
		strength = 0
	}
	if strength > 100 {
		strength = 100
	}
	score = strength*6/10 - congestion*4/10
	return congestion, score
}

// recommend 生成建议：白名单切换候选优先，其次是同频段信道迁移建议。
func (m *Manager) recommend(rep *AssessReport) []Recommendation {
	var recs []Recommendation

	// 白名单内信号更优的 SSID（防钓鱼：只推荐白名单）
	m.mu.Lock()
	whitelist := map[string]string{} // ssid -> password（密码仅用于词条存在判断）
	var threshold, margin int
	if m.cfg != nil {
		threshold, margin = m.cfg.Threshold, m.cfg.MinMargin
		for _, w := range m.cfg.Whitelist {
			whitelist[w.SSID] = w.Password
		}
	}
	m.mu.Unlock()

	curRSSI := -200
	curSSID := ""
	if rep.Current != nil {
		curRSSI, curSSID = rep.Current.RSSI, rep.Current.SSID
	}

	bySSID := map[string]*APInfo{}
	for i := range rep.APList {
		ap := &rep.APList[i]
		if ap.SSID == "" {
			continue
		}
		best := bySSID[ap.SSID]
		if best == nil || ap.RSSI > best.RSSI {
			bySSID[ap.SSID] = ap
		}
	}

	for _, ssid := range sortedKeys(whitelist) {
		ap := bySSID[ssid]
		if ap == nil {
			continue
		}
		if ssid == curSSID {
			continue
		}
		if ap.RSSI >= threshold && ap.RSSI > curRSSI+margin {
			recs = append(recs, Recommendation{
				Kind:   "ssid",
				SSID:   ssid,
				Band:   ap.Band,
				Channel: ap.Channel,
				RSSI:   ap.RSSI,
				Reason: fmt.Sprintf("白名单内信号 %ddBm，优于当前 %ddBm 且超过阈值", ap.RSSI, curRSSI),
			})
		}
	}
	// 排序：信号越好越靠前
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].RSSI > recs[j].RSSI })

	// 当前信道拥挤 → 同频段更佳信道提示
	if rep.Current != nil {
		br := rep.Bands[rep.Current.Band]
		if br != nil {
			if cur := br.Channels[rep.Current.Channel]; cur != nil && cur.Congestion >= 60 {
				if best := br.Best; best != nil && best.Channel != rep.Current.Channel && best.Count > 0 {
					recs = append(recs, Recommendation{
						Kind:    "channel",
						Band:    rep.Current.Band,
						Channel: best.Channel,
						Reason: fmt.Sprintf("当前信道 %d 拥挤（%d 个 AP），本频段更优信道 %d（%d 个 AP，最强 %ddBm）",
							rep.Current.Channel, cur.Count, best.Channel, best.Count, best.BestRSSI),
					})
				}
			}
		}
	}
	if len(recs) == 0 && whitelistHas(whitelist, curSSID) {
		recs = append(recs, Recommendation{
			Kind:   "ssid",
			SSID:   curSSID,
			Reason: "当前连接在白名单内且信号正常，无需切换",
		})
	}
	return recs
}

// ---------------------------------------------------------------------------
// 自动切换引擎
// ---------------------------------------------------------------------------

// Run 常驻循环：按 IntervalSec 周期扫描评估，命中阈值时执行白名单切换。
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.engineTick(ctx)
		}
	}
}

func (m *Manager) engineTick(ctx context.Context) {
	m.mu.Lock()
	if m.cfg == nil || !m.cfg.Enabled {
		m.mu.Unlock()
		return
	}
	interval := time.Duration(m.cfg.IntervalSec) * time.Second
	threshold := m.cfg.Threshold
	margin := m.cfg.MinMargin
	iface := m.iface
	if time.Since(m.lastCheck) < interval {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	rep, err := m.Assess(iface)
	if err != nil {
		m.addLog("check", "", "", false, "扫描失败: "+err.Error())
		return
	}
	cur := rep.Current
	if cur == nil {
		return
	}
	// 阈值未触发
	if cur.RSSI > threshold {
		return
	}
	m.mu.Lock()
	if m.cfg == nil || !m.cfg.Enabled {
		m.mu.Unlock()
		return
	}
	whitelist := append([]WhitelistEntry(nil), m.cfg.Whitelist...)
	checkGateway := m.cfg.CheckGateway
	m.mu.Unlock()

	// 在白名单中选信号最强的候选（同频段优先 > 必需超过当前+margin）
	var pick *APInfo
	bySSID := map[string]*APInfo{}
	for i := range rep.APList {
		ap := &rep.APList[i]
		if ap.SSID == "" {
			continue
		}
		if b := bySSID[ap.SSID]; b == nil || ap.RSSI > b.RSSI {
			bySSID[ap.SSID] = ap
		}
	}
	for i := range whitelist {
		w := &whitelist[i]
		if w.SSID == cur.SSID {
			continue
		}
		ap := bySSID[w.SSID]
		if ap == nil || ap.RSSI < threshold || ap.RSSI <= cur.RSSI+margin {
			continue
		}
		if pick == nil || ap.RSSI > pick.RSSI {
			ap2 := *ap
			pick = &ap2
		}
	}
	if pick == nil {
		m.addLog("check", cur.SSID, "", false, "信号低("+fmt.Sprint(cur.RSSI)+"dBm)但无合格白名单候选")
		return
	}

	// 执行切换 + 连通性验证 + 失败回退
	from := cur.SSID
	to := pick.SSID
	if err := m.switchTo(to, checkGateway); err != nil {
		m.addLog("switch", from, to, false, err.Error())
		// 回退原连接
		if from != "" {
			_ = m.switchTo(from, checkGateway)
		}
		return
	}
	m.addLog("switch", from, to, true, fmt.Sprintf("成功切换（原 %ddBm → 新 %ddBm）", cur.RSSI, pick.RSSI))
}

// InWhitelist 判断 SSID 是否在自动切换白名单内（手动切换的准入检查）。
func (m *Manager) InWhitelist(ssid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		return false
	}
	for _, w := range m.cfg.Whitelist {
		if w.SSID == ssid {
			return true
		}
	}
	return false
}

// SwitchTo 手动切换（幂等，失败回滚）。gateway 为空时取默认路由。
func (m *Manager) SwitchTo(ssid, gateway string) error {
	if err := m.switchTo(ssid, gateway); err != nil {
		m.addLog("switch", "", ssid, false, "手动切换失败: "+err.Error())
		return err
	}
	m.addLog("switch", "", ssid, true, "手动切换成功")
	return nil
}

// switchTo 切换到指定 SSID（使用 NetworkManager）；返回错误时未就绪。
func (m *Manager) switchTo(ssid, gateway string) error {
	profile := nmProfilePrefix + ssid
	// 确保 profile 存在
	if !nmProfileExists(profile) {
		// 尝试按白名单记录建立
		if !m.ensureProfile(ssid) {
			return fmt.Errorf("profile %s 不存在且无法建立", profile)
		}
	}
	out, err := m.runPriv(30*time.Second, "nmcli", "connection", "up", profile)
	if err != nil {
		return fmt.Errorf("nmcli up 失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	// 连通性验证（最多 ~25s）
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if nmWiFiConnected() {
			if gateway == "" {
				gateway = defaultGateway()
			}
			if gateway == "" {
				return nil // 无网关可验，但已连上
			}
			if pingOK(gateway) {
				return nil
			}
			time.Sleep(3 * time.Second)
			continue
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("切换后连通性验证失败（%s）", ssid)
}

// SyncProfiles 为白名单内有密码的条目补齐 nmcli profile。
func (m *Manager) SyncProfiles() {
	m.mu.Lock()
	entries := append([]WhitelistEntry(nil), m.cfg.Whitelist...)
	m.mu.Unlock()
	for i := range entries {
		if entries[i].Password != "" {
			m.ensureProfile(entries[i].SSID)
		}
	}
}

// ensureProfile 建立/校验 nmcli WiFi profile（幂等；需要 sudo）。
func (m *Manager) ensureProfile(ssid string) bool {
	name := nmProfilePrefix + ssid
	if nmProfileExists(name) {
		return true
	}
	m.mu.Lock()
	var pw string
	for _, w := range m.cfg.Whitelist {
		if w.SSID == ssid {
			pw = w.Password
		}
	}
	m.mu.Unlock()
	if pw == "" {
		return false
	}
	_, err := m.runPriv(20*time.Second, "nmcli", "connection", "add",
		"type", "wifi", "con-name", name, "ssid", ssid,
		"wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", pw)
	return err == nil
}

// addLog 追加切换日志（带锁）。
func (m *Manager) addLog(event, from, to string, ok bool, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addLogLocked(event, from, to, ok, msg)
}

func (m *Manager) addLogLocked(event, from, to string, ok bool, msg string) {
	e := SwitchLog{Ts: time.Now().Format(time.RFC3339), Event: event, From: from, To: to, OK: ok, Msg: msg}
	m.log = append(m.log, e)
	maxLog := DefaultMaxLog
	if m.cfg != nil && m.cfg.MaxLog > 0 {
		maxLog = m.cfg.MaxLog
	}
	if len(m.log) > maxLog {
		m.log = m.log[len(m.log)-maxLog:]
	}
}

// ---------------------------------------------------------------------------
// iw / nmcli 输出解析
// ---------------------------------------------------------------------------

var (
	reBSS    = regexp.MustCompile(`(?m)^\s*BSS ([0-9a-fA-F:]{17})`)
	reSSID   = regexp.MustCompile(`(?m)^\s*SSID:\s*(.*)$`)
	reFreq   = regexp.MustCompile(`(?m)^\s*freq:\s*(\d+)`)
	reSignal = regexp.MustCompile(`(?m)^\s*signal:\s*(-?\d+(?:\.\d+)?)`)
)

// parseIwScanAPs 解析 `iw dev <iface> scan` 块状输出。
func parseIwScanAPs(out []byte) []APInfo {
	var aps []APInfo
	var cur *APInfo
	flush := func() {
		if cur != nil && cur.SSID != "" {
			cur.compute()
			aps = append(aps, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(line)
		if m := reBSS.FindStringSubmatch(l); m != nil {
			flush()
			cur = &APInfo{BSSID: strings.ToLower(m[1])}
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(l, "SSID:"):
			cur.SSID = strings.TrimSpace(strings.TrimPrefix(l, "SSID:"))
		case strings.HasPrefix(l, "freq:"):
			fmt.Sscanf(strings.Fields(strings.TrimPrefix(l, "freq:"))[0], "%d", &cur.Freq)
		case strings.HasPrefix(l, "signal:"):
			fmt.Sscanf(strings.Fields(strings.TrimPrefix(l, "signal:"))[0], "%d", &cur.RSSI)
		case strings.HasPrefix(l, "RSN:"):
			if cur.Security == "" {
				cur.Security = "WPA2"
			}
		case strings.HasPrefix(l, "WPA:"):
			if cur.Security == "" {
				cur.Security = "WPA"
			}
		case strings.HasPrefix(l, "WEP:"):
			if cur.Security == "" {
				cur.Security = "WEP"
			}
		}
	}
	flush()
	return aps
}

// parseNmcliAPs 解析 `nmcli -t -f SSID,SIGNAL,SECURITY,CHAN dev wifi list`。
func parseNmcliAPs(out []byte) []APInfo {
	var aps []APInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 4 {
			continue
		}
		ssid := parts[0]
		if ssid == "" || ssid == "--" {
			continue
		}
		sig := atoi(parts[1])
		ch := atoi(parts[3])
		ap := APInfo{
			SSID:     ssid,
			Channel:  ch,
			Security: parts[2],
			RSSI:     sig/2 - 100, // NM 百分比 → dBm 近似
		}
		ap.Band = bandOfChannel(ch)
		ap.Freq = freqOfChannel(ch)
		ap.Signal = clamp(sig, 0, 100)
		aps = append(aps, ap)
	}
	return aps
}

// parseIwLink 解析 `iw dev <iface> link`。
func parseIwLink(out []byte) *CurrentLink {
	lk := &CurrentLink{}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "Connected to") {
			parts := strings.Fields(l)
			if len(parts) >= 3 {
				lk.BSSID = strings.ToLower(parts[2])
			}
		} else if strings.HasPrefix(l, "SSID:") {
			lk.SSID = strings.TrimSpace(strings.TrimPrefix(l, "SSID:"))
		} else if strings.HasPrefix(l, "freq:") {
			fmt.Sscanf(strings.Fields(strings.TrimPrefix(l, "freq:"))[0], "%d", &lk.Freq)
		} else if strings.HasPrefix(l, "signal:") {
			fmt.Sscanf(strings.Fields(strings.TrimPrefix(l, "signal:"))[0], "%d", &lk.RSSI)
		} else if strings.HasPrefix(l, "tx bitrate:") {
			lk.Bitrate = strings.TrimSpace(strings.TrimPrefix(l, "tx bitrate:"))
		}
	}
	if lk.SSID == "" && lk.BSSID == "" {
		return nil
	}
	lk.Channel = channelOfFreq(lk.Freq)
	lk.Band = bandOfFreq(lk.Freq)
	lk.Signal = dBm2Signal(lk.RSSI)
	return lk
}

// parseNmcliLink 解析 `nmcli -t -f SSID,SIGNAL,CHAN,DEVICE dev wifi show` 找指定接口。
func parseNmcliLink(out []byte, iface string) *CurrentLink {
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) < 4 {
			continue
		}
		if parts[3] != iface {
			continue
		}
		ssid := parts[0]
		if ssid == "" || ssid == "--" {
			return nil
		}
		sig := atoi(parts[1])
		ch := atoi(parts[2])
		lk := &CurrentLink{
			SSID:    ssid,
			Channel: ch,
			RSSI:    sig/2 - 100,
			Signal:  clamp(sig, 0, 100),
		}
		lk.Band = bandOfChannel(ch)
		lk.Freq = freqOfChannel(ch)
		return lk
	}
	return nil
}

// ---------------------------------------------------------------------------
// 频段/信道换算
// ---------------------------------------------------------------------------

// channelOfFreq 频率(MHz) → 信道号。
func channelOfFreq(freq int) int {
	switch {
	case freq >= 2412 && freq <= 2484:
		return (freq - 2407) / 5 // 2.4G: 1-14
	case freq >= 5005 && freq <= 5825:
		ch := (freq - 5000) / 5
		if ch < 36 {
			return 0
		}
		if ch > 165 {
			ch = (freq - 5950) / 5 // 近似 6G 下沿（精确映射渲染层处理）
		}
		return ch
	case freq >= 5935 && freq <= 7115:
		return (freq - 5950) / 5 // 6G：5965→3? 规范 5955→1：用近似 (freq-5945)/5
	}
	return 0
}

// freqOfChannel 信道号 → 中心频率(MHz)。
func freqOfChannel(ch int) int {
	switch {
	case ch >= 1 && ch <= 14:
		return 2407 + ch*5
	case ch >= 36 && ch <= 165:
		return 5000 + ch*5
	case ch >= 1 && ch <= 233: // 6G 粗略
		return 5950 + ch*5
	}
	return 0
}

// bandOfFreq 频率 → 频段。
func bandOfFreq(freq int) string {
	switch {
	case freq >= 2412 && freq <= 2484:
		return "2.4"
	case freq >= 5000 && freq <= 5825:
		return "5"
	case freq >= 5935 && freq <= 7115:
		return "6"
	}
	return ""
}

// bandOfChannel 信道号 → 频段。
func bandOfChannel(ch int) string {
	switch {
	case ch >= 1 && ch <= 14:
		return "2.4"
	case ch >= 36 && ch <= 165:
		return "5"
	case ch >= 1 && ch <= 233:
		return "6"
	}
	return ""
}

// dBm2Signal dBm → 0-100（-30→100，-90→0，线性）。
func dBm2Signal(rssi int) int {
	return clamp((rssi+90)*100/60, 0, 100)
}

// compute 补齐派生字段（Signal/Band/Channel/Freq）。
func (ap *APInfo) compute() {
	ap.Signal = dBm2Signal(ap.RSSI)
	ap.Channel = channelOfFreq(ap.Freq)
	ap.Band = bandOfFreq(ap.Freq)
	// 有信道无频率的（某些驱动）反向补频率
	if ap.Freq == 0 && ap.Channel > 0 {
		ap.Freq = freqOfChannel(ap.Channel)
	}
}

// dedupAPs 同 BSSID 去重（保留信号最强）。
func dedupAPs(aps []APInfo) []APInfo {
	best := map[string]int{}
	out := make([]APInfo, 0, len(aps))
	for i := range aps {
		ap := aps[i]
		if ap.BSSID == "" {
			out = append(out, ap)
			continue
		}
		if idx, ok := best[ap.BSSID]; ok {
			if ap.RSSI > out[idx].RSSI {
				out[idx] = ap
			}
			continue
		}
		best[ap.BSSID] = len(out)
		out = append(out, ap)
	}
	return out
}

// ---------------------------------------------------------------------------
// 系统辅助
// ---------------------------------------------------------------------------

// runPriv 提权执行：免密 sudo 可用则 sudo，否则以普通用户执行。
// 返回 stdout（失败时也可能有内容）+ error。
func (m *Manager) runPriv(timeout time.Duration, name string, args ...string) ([]byte, error) {
	if m.sudoOK {
		all := append([]string{"-n", name}, args...)
		return runTimeout(timeout, "sudo", all...)
	}
	return runTimeout(timeout, name, args...)
}

// toolExists 判断命令是否存在。
func toolExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// runTimeout 带超时执行外部命令。
func runTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	//nolint:gosec // 受控命令：名称与参数均来自固定调用点
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return ee.Stderr, err
		}
		return nil, err
	}
	return out, nil
}

// sudoNopassOK 探测当前用户免密 sudo（一次探测）。
func sudoNopassOK() bool {
	_, err := runTimeout(5*time.Second, "sudo", "-n", "true")
	return err == nil
}

// nmProfileExists 判断 nmcli profile 是否存在。
func nmProfileExists(profile string) bool {
	out, err := runTimeout(5*time.Second, "nmcli", "-t", "-f", "NAME", "connection", "show")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == profile {
			return true
		}
	}
	return false
}

// nmWiFiConnected 是否有已连接的 WiFi。
func nmWiFiConnected() bool {
	out, err := runTimeout(5*time.Second, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE", "device", "status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 3 && parts[1] == "wifi" && strings.HasPrefix(parts[2], "connected") {
			return true
		}
	}
	return false
}

// defaultGateway 取默认路由网关。
func defaultGateway() string {
	out, err := runTimeout(5*time.Second, "sh", "-c", "ip route show default | awk '{print $3; exit}'")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// pingOK 网关连通性。
func pingOK(host string) bool {
	out, err := runTimeout(8*time.Second, "ping", "-c", "3", "-W", "2", host)
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "3 received") || strings.Contains(string(out), "3 packets received")
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func whitelistHas(m map[string]string, ssid string) bool {
	_, ok := m[ssid]
	return ok
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}