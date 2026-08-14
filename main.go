//go:build windows
// +build windows

package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"unicom/internal/codec"
	"unicom/internal/config"
	"unicom/internal/connection"
	"unicom/internal/devicewatch"
	"unicom/internal/serialwin"
	"unicom/internal/session"
	"unicom/internal/terminalview"
	"unicom/internal/wrapedit"
)

const appName = "UniCom 通信调试助手"

var (
	VERSION    = "0.0.1"
	GIT_HASH   = "unknown"
	BUILD_TIME = "unknown"
	appTitle   = fmt.Sprintf("%s v%s | %s | %s", appName, VERSION, GIT_HASH, BUILD_TIME)
)

type app struct {
	mw                                                                 *walk.MainWindow
	connectionTypeCB, portCB, baudCB, dataCB, parityCB, stopCB, flowCB *walk.ComboBox
	encodingCB, sendEncodingCB, lineEndingCB                           *walk.ComboBox
	openBtn, refreshBtn, sendBtn, clearBtn, saveBtn                    *walk.PushButton
	tcpLocalHostPicker, tcpServerHostPicker, udpLocalHostPicker        *walk.ToolButton
	dtrCB, rtsCB, autoReconnectCB, hexRXCB, timestampCB, autoScrollCB  *walk.CheckBox
	markSenderCB                                                       *walk.CheckBox
	hexTXCB, escapeCB, cycleCB, termModeCB, wrapRXCB, wrapTXCB         *walk.CheckBox
	tcpBindLocalCB, udpBindLocalCB                                     *walk.CheckBox
	receiveTE, sendTE                                                  *wrapedit.Edit
	intervalLE                                                         *walk.LineEdit
	tcpHostLE, tcpPortLE, tcpLocalHostLE, tcpLocalPortLE               *walk.LineEdit
	tcpServerHostLE, tcpServerPortLE                                   *walk.LineEdit
	udpLocalHostLE, udpLocalPortLE, udpRemoteHostLE, udpRemotePortLE   *walk.LineEdit
	statusItem, countersItem                                           *walk.StatusBarItem
	receiveTools, sendPanel, terminalHost, terminalPanel               *walk.Composite
	rightPanel, settingsPanel, serialBasic, tcpBasic, udpBasic         *walk.Composite
	tcpServerBasic                                                     *walk.Composite
	modeSettingsGB                                                     *walk.GroupBox
	mainSplitter, contentSplitter                                      *walk.Splitter
	terminal                                                           *terminalview.TerminalView
	deviceWatcher                                                      *devicewatch.Watcher

	manager             *connection.Manager
	buffer              *session.Buffer
	mu                  sync.Mutex
	connected, opening  bool
	txBytes             uint64
	lastShownRX         uint64
	lastRender          time.Time
	pending             []byte
	renderEncoding      string
	termLastRX          uint64
	termPending         []byte
	termEncoding        string
	cycleStop           chan struct{}
	localIPOptions      []localIPOption
	receiveChunks       []receiveChunk
	receiveChunkBytes   int
	nextReceiveSeq      uint64
	lastMarkedSeq       uint64
	receiveFollowBottom bool
}

type receiveChunk struct {
	sequence uint64
	source   string
	data     []byte
	received time.Time
}

type receiveScrollState struct {
	followBottom bool
	position     wrapedit.ScrollPosition
	selectionMin int
	selectionMax int
}

var encodings = []string{"UTF-8", "GBK", "GB18030", "Big5", "ASCII", "UTF-16LE", "UTF-16BE"}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	a := &app{buffer: session.NewBuffer(2 * 1024 * 1024), receiveFollowBottom: true}
	if err := a.run(); err != nil {
		walk.MsgBox(nil, appTitle, err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
	}
}

func (a *app) run() error {
	windowIcon, err := walk.NewIconFromResourceId(1)
	if err != nil {
		return fmt.Errorf("load application icon: %v", err)
	}
	defer windowIcon.Dispose()
	a.localIPOptions = listLocalIPOptions()

	if err := a.createUI(windowIcon); err != nil {
		return err
	}
	if err := a.mainSplitter.SetFixed(a.settingsPanel, true); err != nil {
		return err
	}
	a.dtrCB.CheckedChanged().Attach(a.dtrChanged)
	a.rtsCB.CheckedChanged().Attach(a.rtsChanged)
	a.attachEndpointInputHandlers()
	a.receiveTE.SizeChanged().Attach(a.updateReceiveWrap)
	a.receiveTE.ViewportChanged().Attach(func() {
		a.receiveFollowBottom = a.receiveTE.IsScrolledToBottom()
	})
	a.sendTE.SizeChanged().Attach(a.updateSendWrap)
	if err := a.createTerminal(); err != nil {
		return err
	}
	a.manager = connection.New(a.onData, a.onConnectionEvent, a.onTX)
	watcher, err := devicewatch.New(a.mw, func() {
		a.manager.DeviceChanged()
		a.refreshPorts()
	})
	if err != nil {
		return fmt.Errorf("device notification initialization failed: %v", err)
	}
	a.deviceWatcher = watcher
	a.loadSettings()
	a.refreshPorts()
	a.updateControls()
	a.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) { a.stopCycle(); a.manager.Close(); a.saveSettings() })
	go a.periodicUI()
	a.mw.Show()
	a.updateReceiveWrap()
	a.updateSendWrap()
	a.mw.Run()
	return nil
}

func (a *app) createUI(windowIcon *walk.Icon) error {
	return MainWindow{
		AssignTo: &a.mw, Title: appTitle, MinSize: Size{Width: 900, Height: 600}, Size: Size{Width: 1180, Height: 760},
		Icon: windowIcon,
		Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}, Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 6},
		StatusBarItems: []StatusBarItem{{AssignTo: &a.statusItem, Text: "连接已关闭", Width: 620}, {AssignTo: &a.countersItem, Text: "RX 0 B   TX 0 B", Width: 260}},
		Children: []Widget{
			HSplitter{AssignTo: &a.mainSplitter, Name: "mainSplitter", HandleWidth: 7, StretchFactor: 1, Children: []Widget{
				Composite{AssignTo: &a.settingsPanel, MinSize: Size{Width: 260}, MaxSize: Size{Width: 420}, StretchFactor: 2, Layout: VBox{MarginsZero: true, Spacing: 8}, Children: []Widget{
					GroupBox{Title: "连接模式", Layout: VBox{Margins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 10}}, Children: []Widget{
						ComboBox{AssignTo: &a.connectionTypeCB, Model: []string{"串口", "TCP 客户端", "TCP 服务端", "UDP"}, OnCurrentIndexChanged: a.connectionTypeChanged},
					}},
					GroupBox{AssignTo: &a.modeSettingsGB, Title: "模式设置", Layout: VBox{Margins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 10}}, Children: []Widget{
						Composite{AssignTo: &a.serialBasic, Layout: Grid{Columns: 2, MarginsZero: true, Spacing: 7}, Children: []Widget{
							Label{Text: "端口号", Row: 0, Column: 0}, Composite{Row: 0, Column: 1, Layout: HBox{MarginsZero: true, Spacing: 5}, Children: []Widget{
								ComboBox{AssignTo: &a.portCB, Editable: true, Model: []string{}, StretchFactor: 1}, PushButton{AssignTo: &a.refreshBtn, Text: "刷新", OnClicked: a.refreshPorts},
							}},
							Label{Text: "波特率", Row: 1, Column: 0}, ComboBox{AssignTo: &a.baudCB, Editable: true, Model: []string{"1200", "2400", "4800", "9600", "19200", "38400", "57600", "115200", "230400", "460800", "921600"}, Row: 1, Column: 1},
							Label{Text: "数据位", Row: 2, Column: 0}, ComboBox{AssignTo: &a.dataCB, Model: []string{"5", "6", "7", "8"}, Row: 2, Column: 1},
							Label{Text: "校验位", Row: 3, Column: 0}, ComboBox{AssignTo: &a.parityCB, Model: []string{"无", "奇", "偶", "Mark", "Space"}, Row: 3, Column: 1},
							Label{Text: "停止位", Row: 4, Column: 0}, ComboBox{AssignTo: &a.stopCB, Model: []string{"1", "1.5", "2"}, Row: 4, Column: 1},
							Label{Text: "流控制", Row: 5, Column: 0}, ComboBox{AssignTo: &a.flowCB, Model: []string{"无", "RTS/CTS", "XON/XOFF"}, Row: 5, Column: 1},
							Label{Text: "DTR 控制", Row: 6, Column: 0}, CheckBox{AssignTo: &a.dtrCB, Text: "启用", Row: 6, Column: 1},
							Label{Text: "RTS 控制", Row: 7, Column: 0}, CheckBox{AssignTo: &a.rtsCB, Text: "启用", Row: 7, Column: 1},
						}},
						Composite{AssignTo: &a.tcpBasic, Visible: false, Layout: Grid{Columns: 2, MarginsZero: true, Spacing: 7}, Children: []Widget{
							Label{Text: "服务器 IP", Row: 0, Column: 0}, LineEdit{AssignTo: &a.tcpHostLE, Text: "127.0.0.1", Row: 0, Column: 1},
							Label{Text: "服务器端口", Row: 1, Column: 0}, LineEdit{AssignTo: &a.tcpPortLE, Text: "8080", MaxLength: 5, Row: 1, Column: 1},
							CheckBox{AssignTo: &a.tcpBindLocalCB, Text: "指定本地地址", Row: 2, Column: 0, ColumnSpan: 2, OnCheckedChanged: a.updateControls},
							Label{Text: "本地 IP", Row: 3, Column: 0}, Composite{Row: 3, Column: 1, Layout: HBox{MarginsZero: true, SpacingZero: true}, Children: []Widget{
								LineEdit{AssignTo: &a.tcpLocalHostLE, Text: "0.0.0.0", StretchFactor: 1},
								ToolButton{AssignTo: &a.tcpLocalHostPicker, Text: "\u25be", MinSize: Size{Width: 28}, MaxSize: Size{Width: 28}, ToolTipText: "选择本机 IP", ContextMenuItems: a.localIPMenuItems(&a.tcpLocalHostLE), OnClicked: func() { showLocalIPMenu(a.tcpLocalHostPicker) }},
							}},
							Label{Text: "本地端口", Row: 4, Column: 0}, LineEdit{AssignTo: &a.tcpLocalPortLE, Text: "8080", MaxLength: 5, Row: 4, Column: 1},
						}},
						Composite{AssignTo: &a.tcpServerBasic, Visible: false, Layout: Grid{Columns: 2, MarginsZero: true, Spacing: 7}, Children: []Widget{
							Label{Text: "监听 IP", Row: 0, Column: 0}, Composite{Row: 0, Column: 1, Layout: HBox{MarginsZero: true, SpacingZero: true}, Children: []Widget{
								LineEdit{AssignTo: &a.tcpServerHostLE, Text: "0.0.0.0", StretchFactor: 1},
								ToolButton{AssignTo: &a.tcpServerHostPicker, Text: "\u25be", MinSize: Size{Width: 28}, MaxSize: Size{Width: 28}, ToolTipText: "选择本机 IP", ContextMenuItems: a.localIPMenuItems(&a.tcpServerHostLE), OnClicked: func() { showLocalIPMenu(a.tcpServerHostPicker) }},
							}},
							Label{Text: "监听端口", Row: 1, Column: 0}, LineEdit{AssignTo: &a.tcpServerPortLE, Text: "8080", MaxLength: 5, Row: 1, Column: 1},
						}},
						Composite{AssignTo: &a.udpBasic, Visible: false, Layout: Grid{Columns: 2, MarginsZero: true, Spacing: 7}, Children: []Widget{
							Label{Text: "对端 IP", Row: 0, Column: 0}, LineEdit{AssignTo: &a.udpRemoteHostLE, Text: "127.0.0.1", Row: 0, Column: 1},
							Label{Text: "对端端口", Row: 1, Column: 0}, LineEdit{AssignTo: &a.udpRemotePortLE, Text: "8080", MaxLength: 5, Row: 1, Column: 1},
							CheckBox{AssignTo: &a.udpBindLocalCB, Text: "指定本地地址", Row: 2, Column: 0, ColumnSpan: 2, OnCheckedChanged: a.updateControls},
							Label{Text: "本地 IP", Row: 3, Column: 0}, Composite{Row: 3, Column: 1, Layout: HBox{MarginsZero: true, SpacingZero: true}, Children: []Widget{
								LineEdit{AssignTo: &a.udpLocalHostLE, Text: "0.0.0.0", StretchFactor: 1},
								ToolButton{AssignTo: &a.udpLocalHostPicker, Text: "\u25be", MinSize: Size{Width: 28}, MaxSize: Size{Width: 28}, ToolTipText: "选择本机 IP", ContextMenuItems: a.localIPMenuItems(&a.udpLocalHostLE), OnClicked: func() { showLocalIPMenu(a.udpLocalHostPicker) }},
							}},
							Label{Text: "本地端口", Row: 4, Column: 0}, LineEdit{AssignTo: &a.udpLocalPortLE, Text: "8080", MaxLength: 5, Row: 4, Column: 1},
						}},
					}},
					VSpacer{},
					GroupBox{Title: "公共设置", Layout: Grid{Columns: 2, Margins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 10}, Spacing: 7}, Children: []Widget{
						Label{Text: "接收编码", Row: 0, Column: 0}, ComboBox{AssignTo: &a.encodingCB, Model: encodings, Row: 0, Column: 1, OnCurrentIndexChanged: a.renderAll},
						CheckBox{AssignTo: &a.autoReconnectCB, Text: "断线自动重连", Checked: true, Row: 1, Column: 0, ColumnSpan: 2},
						PushButton{AssignTo: &a.openBtn, Text: "打开串口", MinSize: Size{Height: 34}, Row: 2, Column: 0, ColumnSpan: 2, OnClicked: a.togglePort},
					}},
				}},
				Composite{AssignTo: &a.rightPanel, StretchFactor: 7, Layout: VBox{MarginsZero: true}, Children: []Widget{
					VSplitter{AssignTo: &a.contentSplitter, Name: "rxTxSplitter", HandleWidth: 7, Children: []Widget{
						GroupBox{Title: "接收区", StretchFactor: 3, MinSize: Size{Height: 150}, Layout: HBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 8}, Children: []Widget{
							Composite{MinSize: Size{Width: 220}, StretchFactor: 1000, Layout: VBox{MarginsZero: true}, Children: []Widget{
								wrapedit.View{AssignTo: &a.receiveTE, ReadOnly: true, VScroll: true, MaxLength: 4 * 1024 * 1024, Font: Font{Family: "Consolas", PointSize: 10}},
							}},
							Composite{AssignTo: &a.receiveTools, MinSize: Size{Width: 156}, MaxSize: Size{Width: 156}, Layout: VBox{MarginsZero: true, Spacing: 7}, Children: []Widget{
								Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{CheckBox{AssignTo: &a.termModeCB, Text: "终端模式", OnCheckedChanged: a.termModeChanged}, HSpacer{}}},
								Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{CheckBox{AssignTo: &a.autoScrollCB, Text: "自动滚动", Checked: true}, HSpacer{}}},
								Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{CheckBox{AssignTo: &a.timestampCB, Text: "显示接收时间戳", OnCheckedChanged: a.renderAll}, HSpacer{}}},
								Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{CheckBox{AssignTo: &a.markSenderCB, Text: "标记发送者", OnCheckedChanged: a.renderAll}, HSpacer{}}},
								Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{CheckBox{AssignTo: &a.hexRXCB, Text: "HEX 显示", OnCheckedChanged: a.renderAll}, HSpacer{}}},
								Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{CheckBox{AssignTo: &a.wrapRXCB, Text: "自动换行", Checked: true, OnCheckedChanged: a.updateReceiveWrap}, HSpacer{}}},
								VSpacer{},
								PushButton{AssignTo: &a.clearBtn, Text: "清空 RX 区", OnClicked: a.clearReceive},
								PushButton{AssignTo: &a.saveBtn, Text: "保存接收数据", OnClicked: a.saveReceive},
							}},
						}},
						GroupBox{Title: "发送区", StretchFactor: 2, MinSize: Size{Height: 250}, Layout: HBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 8}, Children: []Widget{
							wrapedit.View{AssignTo: &a.sendTE, MinSize: Size{Width: 220, Height: 90}, StretchFactor: 1, VScroll: true, Font: Font{Family: "Consolas", PointSize: 10}},
							Composite{AssignTo: &a.sendPanel, MinSize: Size{Width: 156}, MaxSize: Size{Width: 156}, Layout: Grid{Columns: 2, MarginsZero: true, Spacing: 6}, Children: []Widget{
								CheckBox{AssignTo: &a.escapeCB, Text: "启用转义", Row: 0, Column: 0, ColumnSpan: 2},
								CheckBox{AssignTo: &a.hexTXCB, Text: "HEX", Row: 1, Column: 0, OnCheckedChanged: a.updateEscapeState},
								CheckBox{AssignTo: &a.wrapTXCB, Text: "换行", Checked: true, Row: 1, Column: 1, OnCheckedChanged: a.updateSendWrap},
								Label{Text: "编码", Row: 2, Column: 0}, ComboBox{AssignTo: &a.sendEncodingCB, Model: encodings, Row: 2, Column: 1},
								Label{Text: "行尾", Row: 3, Column: 0}, ComboBox{AssignTo: &a.lineEndingCB, Model: []string{"无", "CR", "LF", "CRLF"}, Row: 3, Column: 1},
								CheckBox{AssignTo: &a.cycleCB, Text: "周期发送", Row: 4, Column: 0, ColumnSpan: 2, OnCheckedChanged: a.cycleChanged},
								Label{Text: "间隔", Row: 5, Column: 0}, Composite{Row: 5, Column: 1, Layout: HBox{MarginsZero: true, Spacing: 4}, Children: []Widget{LineEdit{AssignTo: &a.intervalLE, Text: "1000", MaxLength: 7, StretchFactor: 1}, Label{Text: "ms"}}},
								VSpacer{Row: 6, Column: 0, ColumnSpan: 2},
								PushButton{AssignTo: &a.sendBtn, Text: "发送", MinSize: Size{Height: 34}, Row: 7, Column: 0, ColumnSpan: 2, OnClicked: a.send},
							}},
						}},
					}},
					Composite{AssignTo: &a.terminalPanel, Visible: false, Layout: VBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
						Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
							HSpacer{}, PushButton{Text: "退出终端模式", MinSize: Size{Width: 112}, OnClicked: func() { a.termModeCB.SetChecked(false) }},
						}},
						Composite{AssignTo: &a.terminalHost, StretchFactor: 1, Layout: VBox{MarginsZero: true}},
					}},
				}},
			}},
		},
	}.Create()
}

func (a *app) createTerminal() error {
	view, err := terminalview.New(a.terminalHost, a.termSendBytes, a.termSendText)
	if err != nil {
		return err
	}
	a.terminal = view
	return nil
}

func (a *app) serialConfig() (serialwin.Config, error) {
	baud, err := strconv.Atoi(strings.TrimSpace(a.baudCB.Text()))
	if err != nil || baud <= 0 {
		return serialwin.Config{}, fmt.Errorf("波特率无效")
	}
	data, _ := strconv.Atoi(a.dataCB.Text())
	if data < 5 || data > 8 {
		return serialwin.Config{}, fmt.Errorf("数据位无效")
	}
	return serialwin.Config{Name: strings.ToUpper(strings.TrimSpace(a.portCB.Text())), BaudRate: uint32(baud), DataBits: byte(data), Parity: byte(a.parityCB.CurrentIndex()), StopBits: byte(a.stopCB.CurrentIndex()), Flow: byte(a.flowCB.CurrentIndex()), DTR: a.dtrCB.Checked(), RTS: a.rtsCB.Checked()}, nil
}

func (a *app) togglePort() {
	a.mu.Lock()
	opening := a.opening
	a.mu.Unlock()
	if opening {
		a.manager.Close()
		return
	}
	a.mu.Lock()
	a.opening = true
	a.mu.Unlock()
	a.updateControls()
	if a.isTCPServer() {
		address, err := endpointAddress(a.tcpServerHostLE.Text(), a.tcpServerPortLE.Text(), "监听")
		if err != nil {
			a.openFailed(err)
			return
		}
		a.manager.OpenTCPServer(address)
		return
	}
	if a.isUDP() {
		localAddress, remoteAddress, err := a.udpAddresses()
		if err != nil {
			a.openFailed(err)
			return
		}
		a.manager.OpenUDP(localAddress, remoteAddress)
		return
	}
	if a.isTCP() {
		localAddress, address, err := a.tcpAddresses()
		if err != nil {
			a.openFailed(err)
			return
		}
		a.manager.OpenTCP(localAddress, address)
		return
	}
	c, err := a.serialConfig()
	if err != nil {
		a.mu.Lock()
		a.opening = false
		a.mu.Unlock()
		a.updateControls()
		a.showError(err)
		return
	}
	a.manager.Open(c)
}

func (a *app) openFailed(err error) {
	a.mu.Lock()
	a.opening = false
	a.mu.Unlock()
	a.updateControls()
	a.showError(err)
}

func (a *app) isTCP() bool       { return a.connectionTypeCB.CurrentIndex() == 1 }
func (a *app) isTCPServer() bool { return a.connectionTypeCB.CurrentIndex() == 2 }
func (a *app) isUDP() bool       { return a.connectionTypeCB.CurrentIndex() == 3 }
func (a *app) isNetwork() bool   { return a.isTCP() || a.isUDP() || a.isTCPServer() }

type endpointTextInput interface {
	Text() string
	SetText(string) error
	KeyDown() *walk.KeyEvent
}

func (a *app) attachEndpointInputHandlers() {
	pairs := []struct {
		host endpointTextInput
		port *walk.LineEdit
	}{
		{a.tcpHostLE, a.tcpPortLE},
		{a.tcpLocalHostLE, a.tcpLocalPortLE},
		{a.tcpServerHostLE, a.tcpServerPortLE},
		{a.udpRemoteHostLE, a.udpRemotePortLE},
		{a.udpLocalHostLE, a.udpLocalPortLE},
	}
	for _, pair := range pairs {
		hostEdit, portEdit := pair.host, pair.port
		hostEdit.KeyDown().Attach(func(key walk.Key) {
			if key != walk.KeyReturn {
				return
			}
			host, port, hasPort := normalizeEndpointInput(hostEdit.Text())
			if host != hostEdit.Text() {
				hostEdit.SetText(host)
			}
			if hasPort && port != portEdit.Text() {
				portEdit.SetText(port)
			}
		})
	}
}

func (a *app) localIPMenuItems(target **walk.LineEdit) []MenuItem {
	items := make([]MenuItem, 0, len(a.localIPOptions))
	for _, option := range a.localIPOptions {
		ip := option.IP
		items = append(items, Action{
			Text: option.Label,
			OnTriggered: func() {
				if *target != nil {
					(*target).SetText(ip)
				}
			},
		})
	}
	return items
}

func showLocalIPMenu(button *walk.ToolButton) {
	if button == nil || button.ContextMenu() == nil {
		return
	}
	p := win.POINT{Y: int32(button.ClientBounds().Height)}
	if !win.ClientToScreen(button.Handle(), &p) {
		return
	}
	button.SendMessage(
		win.WM_CONTEXTMENU,
		uintptr(button.Handle()),
		uintptr(win.MAKELONG(uint16(p.X), uint16(p.Y))),
	)
}

func (a *app) networkAddress() (string, error) {
	host := strings.TrimSpace(a.tcpHostLE.Text())
	if host == "" {
		return "", fmt.Errorf("服务器地址不能为空")
	}
	port, err := strconv.Atoi(strings.TrimSpace(a.tcpPortLE.Text()))
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("端口必须在 1 到 65535 之间")
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func (a *app) tcpAddresses() (string, string, error) {
	remote, err := a.networkAddress()
	if err != nil {
		return "", "", err
	}
	if !a.tcpBindLocalCB.Checked() {
		return "", remote, nil
	}
	local, err := endpointAddress(a.tcpLocalHostLE.Text(), a.tcpLocalPortLE.Text(), "本地")
	if err != nil {
		return "", "", err
	}
	return local, remote, nil
}

func (a *app) udpAddresses() (string, string, error) {
	remote, err := endpointAddress(a.udpRemoteHostLE.Text(), a.udpRemotePortLE.Text(), "对端")
	if err != nil {
		return "", "", err
	}
	if !a.udpBindLocalCB.Checked() {
		return "", remote, nil
	}
	local, err := endpointAddress(a.udpLocalHostLE.Text(), a.udpLocalPortLE.Text(), "本地")
	if err != nil {
		return "", "", err
	}
	return local, remote, nil
}

func endpointAddress(hostText, portText, name string) (string, error) {
	host := strings.TrimSpace(hostText)
	if host == "" {
		return "", fmt.Errorf("%s IP 不能为空", name)
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("%s端口必须在 1 到 65535 之间", name)
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func (a *app) onConnectionEvent(e connection.Event) {
	a.mw.Synchronize(func() {
		if e.State == connection.StateWaiting && !a.isTCPServer() && !a.autoReconnectCB.Checked() {
			go a.manager.Close()
			return
		}
		a.mu.Lock()
		connected := e.State == connection.StateConnected
		if a.isTCPServer() && e.State != connection.StateClosed {
			connected = a.manager.Connected()
		}
		a.connected = connected
		a.opening = e.State != connection.StateClosed
		a.mu.Unlock()
		a.statusItem.SetText(e.Message)
		a.updateControls()
	})
}

func (a *app) onData(event connection.DataEvent) {
	a.mu.Lock()
	a.buffer.Append(event.Data)
	a.nextReceiveSeq++
	a.receiveChunks = append(a.receiveChunks, receiveChunk{
		sequence: a.nextReceiveSeq,
		source:   event.Source,
		data:     event.Data,
		received: time.Now(),
	})
	a.receiveChunkBytes += len(event.Data)
	for a.receiveChunkBytes > 2*1024*1024 && len(a.receiveChunks) > 1 {
		a.receiveChunkBytes -= len(a.receiveChunks[0].data)
		a.receiveChunks = a.receiveChunks[1:]
	}
	a.mu.Unlock()
	a.scheduleRender()
}
func (a *app) onTX(n int) { a.mu.Lock(); a.txBytes += uint64(n); a.mu.Unlock() }
func (a *app) scheduleRender() {
	a.mu.Lock()
	if time.Since(a.lastRender) < 60*time.Millisecond {
		a.mu.Unlock()
		return
	}
	a.lastRender = time.Now()
	a.mu.Unlock()
	a.mw.Synchronize(a.renderPending)
}

func (a *app) captureReceiveScroll() receiveScrollState {
	if a.receiveTE == nil {
		return receiveScrollState{}
	}
	selectionMin, selectionMax := a.receiveTE.TextSelection()
	return receiveScrollState{
		followBottom: a.autoScrollCB != nil && a.autoScrollCB.Checked() && a.receiveFollowBottom && !a.receiveTE.MouseSelecting(),
		position:     a.receiveTE.ScrollPosition(),
		selectionMin: selectionMin,
		selectionMax: selectionMax,
	}
}

func (a *app) restoreReceiveScroll(state receiveScrollState, restoreSelection bool) {
	if restoreSelection {
		length := a.receiveTE.TextLength()
		if state.selectionMin > length {
			state.selectionMin = length
		}
		if state.selectionMax > length {
			state.selectionMax = length
		}
		a.receiveTE.SetTextSelection(state.selectionMin, state.selectionMax)
	}
	if state.followBottom {
		a.receiveTE.ScrollToBottom()
		a.receiveFollowBottom = true
		return
	}
	a.receiveTE.SetScrollPosition(state.position)
}

func (a *app) renderAll() {
	data, rx := a.buffer.Snapshot()
	if a.termModeCB != nil && a.termModeCB.Checked() {
		a.replayTerminal(data, rx)
		return
	}
	if a.showSenderMarkers() {
		a.renderAllWithSenders()
		return
	}
	var text string
	if a.hexRXCB.Checked() {
		text = codec.HexDump(data)
	} else {
		text = codec.Decode(data, a.selectedEncoding())
	}
	if a.timestampCB.Checked() && text != "" {
		lines := strings.Split(strings.Replace(text, "\r\n", "\n", -1), "\n")
		var b strings.Builder
		for i, line := range lines {
			if i == len(lines)-1 && line == "" {
				continue
			}
			b.WriteString("[")
			b.WriteString(time.Now().Format("15:04:05.000"))
			b.WriteString("] ")
			b.WriteString(line)
			b.WriteString("\r\n")
		}
		text = b.String()
	}
	a.pending = a.pending[:0]
	a.renderEncoding = a.selectedEncoding()
	scroll := a.captureReceiveScroll()
	a.receiveTE.SetText(text)
	a.restoreReceiveScroll(scroll, true)
	a.lastShownRX = rx
	a.updateCounters(rx)
}

func (a *app) renderPending() {
	if a.receiveTE != nil && a.receiveTE.MouseSelecting() {
		return
	}
	if a.termModeCB != nil && a.termModeCB.Checked() {
		a.renderTerminalPending()
		return
	}
	if a.showSenderMarkers() {
		a.renderSenderPending()
		return
	}
	if a.timestampCB.Checked() || a.renderEncoding != a.selectedEncoding() {
		a.renderAll()
		return
	}
	data, rx, reset := a.buffer.Delta(a.lastShownRX)
	if reset || a.receiveTE.TextLength() > 2*1024*1024 {
		a.renderAll()
		return
	}
	if len(data) == 0 {
		a.updateCounters(rx)
		return
	}
	if a.hexRXCB.Checked() {
		text := codec.HexDump(data)
		if a.receiveTE.TextLength() > 0 && text != "" {
			text = " " + text
		}
		scroll := a.captureReceiveScroll()
		a.receiveTE.AppendText(text)
		a.restoreReceiveScroll(scroll, false)
		a.lastShownRX = rx
		a.updateCounters(rx)
		return
	}
	data = append(a.pending, data...)
	n := codec.CompletePrefix(data, a.selectedEncoding())
	a.pending = append(a.pending[:0], data[n:]...)
	if n > 0 {
		scroll := a.captureReceiveScroll()
		a.receiveTE.AppendText(codec.Decode(data[:n], a.selectedEncoding()))
		a.restoreReceiveScroll(scroll, false)
	}
	a.lastShownRX = rx
	a.updateCounters(rx)
}

func (a *app) showSenderMarkers() bool {
	return a.markSenderCB != nil && a.markSenderCB.Checked() && (a.isTCPServer() || a.isUDP())
}

func (a *app) receiveChunksSnapshot() ([]receiveChunk, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	chunks := make([]receiveChunk, len(a.receiveChunks))
	copy(chunks, a.receiveChunks)
	return chunks, a.buffer.Received()
}

func (a *app) formatReceiveChunk(chunk receiveChunk) string {
	var b strings.Builder
	if a.timestampCB.Checked() {
		b.WriteString("[")
		b.WriteString(chunk.received.Format("15:04:05.000"))
		b.WriteString("] ")
	}
	if chunk.source != "" {
		b.WriteString("[")
		b.WriteString(chunk.source)
		b.WriteString("] ")
	}
	var payload string
	if a.hexRXCB.Checked() {
		payload = codec.HexDump(chunk.data)
	} else {
		payload = codec.Decode(chunk.data, a.selectedEncoding())
	}
	b.WriteString(payload)
	if !strings.HasSuffix(payload, "\r") && !strings.HasSuffix(payload, "\n") {
		b.WriteString("\r\n")
	}
	return b.String()
}

func (a *app) renderAllWithSenders() {
	chunks, rx := a.receiveChunksSnapshot()
	var b strings.Builder
	for _, chunk := range chunks {
		b.WriteString(a.formatReceiveChunk(chunk))
	}
	a.pending = a.pending[:0]
	a.renderEncoding = a.selectedEncoding()
	scroll := a.captureReceiveScroll()
	a.receiveTE.SetText(b.String())
	if len(chunks) > 0 {
		a.lastMarkedSeq = chunks[len(chunks)-1].sequence
	} else {
		a.lastMarkedSeq = 0
	}
	a.lastShownRX = rx
	a.updateCounters(rx)
	a.restoreReceiveScroll(scroll, true)
}

func (a *app) renderSenderPending() {
	chunks, rx := a.receiveChunksSnapshot()
	if len(chunks) == 0 {
		a.updateCounters(rx)
		return
	}
	if a.lastMarkedSeq != 0 && chunks[0].sequence > a.lastMarkedSeq+1 {
		a.renderAllWithSenders()
		return
	}
	var b strings.Builder
	for _, chunk := range chunks {
		if chunk.sequence <= a.lastMarkedSeq {
			continue
		}
		b.WriteString(a.formatReceiveChunk(chunk))
		a.lastMarkedSeq = chunk.sequence
	}
	if b.Len() > 0 {
		scroll := a.captureReceiveScroll()
		a.receiveTE.AppendText(b.String())
		a.restoreReceiveScroll(scroll, false)
	}
	a.lastShownRX = rx
	a.updateCounters(rx)
	if a.receiveTE.TextLength() > 2*1024*1024 {
		a.renderAllWithSenders()
	}
}

func (a *app) send() {
	b, err := a.makeSendData()
	if err != nil {
		a.showError(err)
		return
	}
	if len(b) == 0 {
		return
	}
	if err = a.manager.Write(b); err != nil {
		a.showError(err)
	}
}
func (a *app) makeSendData() ([]byte, error) {
	if a.hexTXCB.Checked() {
		return codec.ParseHex(a.sendTE.Text())
	}
	text := a.sendTE.Text()
	var err error
	if a.escapeCB.Checked() {
		text, err = codec.Unescape(text)
		if err != nil {
			return nil, err
		}
	}
	b, err := codec.Encode(text, a.sendEncodingCB.Text())
	if err != nil {
		return nil, err
	}
	switch a.lineEndingCB.CurrentIndex() {
	case 1:
		b = append(b, '\r')
	case 2:
		b = append(b, '\n')
	case 3:
		b = append(b, '\r', '\n')
	}
	return b, nil
}

func (a *app) cycleChanged() {
	if a.cycleCB.Checked() {
		a.startCycle()
	} else {
		a.stopCycle()
	}
}
func (a *app) startCycle() {
	a.stopCycle()
	ms, err := strconv.Atoi(strings.TrimSpace(a.intervalLE.Text()))
	if err != nil || ms < 20 {
		a.cycleCB.SetChecked(false)
		a.showError(fmt.Errorf("周期不能小于 20ms"))
		return
	}
	stop := make(chan struct{})
	a.cycleStop = stop
	go func() {
		ticker := time.NewTicker(time.Duration(ms) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				a.mw.Synchronize(a.send)
			case <-stop:
				return
			}
		}
	}()
}
func (a *app) stopCycle() {
	if a.cycleStop != nil {
		close(a.cycleStop)
		a.cycleStop = nil
	}
}

func (a *app) refreshPorts() {
	current := ""
	if a.portCB != nil {
		current = a.portCB.Text()
	}
	ports := serialwin.ListPorts()
	a.portCB.SetModel(ports)
	if current != "" {
		a.portCB.SetText(current)
	} else if len(ports) > 0 {
		a.portCB.SetCurrentIndex(0)
	}
}

func (a *app) connectionTypeChanged() {
	if a.serialBasic == nil || a.tcpBasic == nil || a.udpBasic == nil || a.tcpServerBasic == nil || a.modeSettingsGB == nil || a.settingsPanel == nil {
		return
	}
	a.serialBasic.SetVisible(!a.isNetwork())
	a.tcpBasic.SetVisible(a.isTCP())
	a.tcpServerBasic.SetVisible(a.isTCPServer())
	a.udpBasic.SetVisible(a.isUDP())
	a.modeSettingsGB.Layout().Update(true)
	a.settingsPanel.Layout().Update(true)
	a.updateControls()
	if a.receiveTE != nil {
		a.renderAll()
	}
}
func (a *app) clearReceive() {
	a.mu.Lock()
	a.buffer.Clear()
	a.receiveChunks = nil
	a.receiveChunkBytes = 0
	a.lastMarkedSeq = 0
	a.mu.Unlock()
	a.pending = a.pending[:0]
	a.termPending = a.termPending[:0]
	a.termLastRX = 0
	a.receiveTE.SetText("")
	a.receiveFollowBottom = true
	if a.terminal != nil {
		a.terminal.Reset()
	}
	a.lastShownRX = 0
	a.updateCounters(0)
}
func (a *app) saveReceive() {
	dlg := new(walk.FileDialog)
	dlg.Title = "保存接收数据"
	dlg.Filter = "文本文件 (*.txt)|*.txt|原始数据 (*.bin)|*.bin|所有文件 (*.*)|*.*"
	dlg.FilePath = "unicom-" + time.Now().Format("20060102-150405") + ".txt"
	ok, err := dlg.ShowSave(a.mw)
	if err != nil || !ok {
		return
	}
	var data []byte
	if strings.HasSuffix(strings.ToLower(dlg.FilePath), ".bin") {
		data, _ = a.buffer.Snapshot()
	} else if a.termModeCB.Checked() && a.terminal != nil {
		data = []byte(a.terminal.Text())
	} else {
		data = []byte(a.receiveTE.Text())
	}
	if err = ioutil.WriteFile(dlg.FilePath, data, 0644); err != nil {
		a.showError(err)
		return
	}
	a.statusItem.SetText("已保存: " + dlg.FilePath)
}
func (a *app) updateControls() {
	a.mu.Lock()
	opening, connected := a.opening, a.connected
	a.mu.Unlock()
	mode := "串口"
	if a.isTCP() {
		mode = "TCP 客户端"
	} else if a.isUDP() {
		mode = "UDP"
	} else if a.isTCPServer() {
		mode = "TCP 服务端"
	}
	a.openBtn.SetText(map[bool]string{true: "关闭" + mode, false: "打开" + mode}[opening])
	a.sendBtn.SetEnabled(connected)
	a.connectionTypeCB.SetEnabled(!opening)
	a.autoReconnectCB.SetEnabled(!opening && !a.isTCPServer())
	a.markSenderCB.SetEnabled(a.isTCPServer() || a.isUDP())
	a.refreshBtn.SetEnabled(!opening && !a.isNetwork())
	for _, w := range []walk.Widget{a.portCB, a.baudCB, a.dataCB, a.parityCB, a.stopCB, a.flowCB} {
		w.SetEnabled(!opening)
	}
	a.tcpHostLE.SetEnabled(!opening)
	a.tcpPortLE.SetEnabled(!opening)
	a.tcpBindLocalCB.SetEnabled(!opening)
	a.tcpLocalHostLE.SetEnabled(!opening && a.tcpBindLocalCB.Checked())
	a.tcpLocalHostPicker.SetEnabled(!opening && a.tcpBindLocalCB.Checked())
	a.tcpLocalPortLE.SetEnabled(!opening && a.tcpBindLocalCB.Checked())
	a.udpRemoteHostLE.SetEnabled(!opening)
	a.udpRemotePortLE.SetEnabled(!opening)
	a.udpBindLocalCB.SetEnabled(!opening)
	a.udpLocalHostLE.SetEnabled(!opening && a.udpBindLocalCB.Checked())
	a.udpLocalHostPicker.SetEnabled(!opening && a.udpBindLocalCB.Checked())
	a.udpLocalPortLE.SetEnabled(!opening && a.udpBindLocalCB.Checked())
	a.tcpServerHostLE.SetEnabled(!opening)
	a.tcpServerHostPicker.SetEnabled(!opening)
	a.tcpServerPortLE.SetEnabled(!opening)
	a.dtrCB.SetEnabled(connected && !a.isNetwork())
	a.rtsCB.SetEnabled(connected && !a.isNetwork())
}

func (a *app) dtrChanged() {
	if a.manager == nil || !a.manager.Connected() {
		return
	}
	if err := a.manager.SetDTR(a.dtrCB.Checked()); err != nil {
		a.statusItem.SetText(err.Error())
	}
}

func (a *app) rtsChanged() {
	if a.manager == nil || !a.manager.Connected() {
		return
	}
	if err := a.manager.SetRTS(a.rtsCB.Checked()); err != nil {
		a.statusItem.SetText(err.Error())
	}
}

func (a *app) updateEscapeState() {
	if a.escapeCB != nil && a.hexTXCB != nil {
		a.escapeCB.SetEnabled(!a.hexTXCB.Checked())
	}
}

func (a *app) updateReceiveWrap() {
	if a.wrapRXCB != nil {
		setTextEditWrap(a.receiveTE, a.wrapRXCB.Checked())
	}
}

func (a *app) updateSendWrap() {
	if a.wrapTXCB != nil {
		setTextEditWrap(a.sendTE, a.wrapTXCB.Checked())
	}
}

func setTextEditWrap(te *wrapedit.Edit, wrap bool) {
	if te == nil || te.Handle() == 0 {
		return
	}
	te.SetWordWrap(wrap)
}

func (a *app) termModeChanged() {
	if a.terminalPanel == nil || a.contentSplitter == nil || a.rightPanel == nil {
		return
	}
	term := a.termModeCB.Checked()
	a.contentSplitter.SetVisible(!term)
	a.terminalPanel.SetVisible(term)
	a.rightPanel.Layout().Update(true)
	if term {
		if a.cycleCB.Checked() {
			a.cycleCB.SetChecked(false)
		}
		a.renderAll()
		if a.terminal != nil {
			a.terminal.SetFocus()
		}
	} else {
		a.renderAll()
	}
}

func (a *app) termSendBytes(p []byte) error {
	if a.manager == nil {
		return fmt.Errorf("连接未建立")
	}
	err := a.manager.Write(p)
	if err != nil && a.statusItem != nil {
		a.statusItem.SetText(err.Error())
	}
	return err
}

func (a *app) termSendText(text string) error {
	b, err := codec.Encode(text, a.selectedEncoding())
	if err != nil {
		return err
	}
	return a.termSendBytes(b)
}

func (a *app) replayTerminal(data []byte, rx uint64) {
	if a.terminal == nil {
		return
	}
	a.termPending = a.termPending[:0]
	a.termEncoding = a.selectedEncoding()
	a.terminal.Reset()
	if len(data) > 0 {
		a.terminal.Feed([]byte(codec.Decode(data, a.termEncoding)))
	}
	a.termLastRX, a.lastShownRX = rx, rx
	a.updateCounters(rx)
}

func (a *app) renderTerminalPending() {
	if a.terminal == nil {
		return
	}
	if a.termEncoding != a.selectedEncoding() {
		data, rx := a.buffer.Snapshot()
		a.replayTerminal(data, rx)
		return
	}
	data, rx, reset := a.buffer.Delta(a.termLastRX)
	if reset {
		all, total := a.buffer.Snapshot()
		a.replayTerminal(all, total)
		return
	}
	if len(data) == 0 {
		a.lastShownRX = rx
		a.updateCounters(rx)
		return
	}
	data = append(a.termPending, data...)
	n := codec.CompletePrefix(data, a.termEncoding)
	a.termPending = append(a.termPending[:0], data[n:]...)
	if n > 0 {
		if err := a.terminal.Feed([]byte(codec.Decode(data[:n], a.termEncoding))); err != nil {
			a.statusItem.SetText(err.Error())
		}
	}
	a.termLastRX, a.lastShownRX = rx, rx
	a.updateCounters(rx)
}
func (a *app) updateCounters(rx uint64) {
	a.mu.Lock()
	tx := a.txBytes
	a.mu.Unlock()
	a.countersItem.SetText(fmt.Sprintf("RX %d B   TX %d B", rx, tx))
}
func (a *app) periodicUI() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if a.mw == nil || a.mw.Handle() == 0 {
			return
		}
		rx := a.buffer.Received()
		a.mw.Synchronize(func() {
			if rx != a.lastShownRX {
				a.renderPending()
			} else {
				a.updateCounters(rx)
			}
		})
	}
}
func (a *app) selectedEncoding() string {
	s := a.encodingCB.Text()
	if s == "" {
		return "UTF-8"
	}
	return s
}
func (a *app) showError(err error) {
	a.statusItem.SetText(err.Error())
	walk.MsgBox(a.mw, appTitle, err.Error(), walk.MsgBoxOK|walk.MsgBoxIconWarning)
}

func (a *app) loadSettings() {
	connectionType := config.LoadInt("ConnectionType", 0)
	if config.LoadInt("ConnectionTypeOrder", 0) == 0 {
		if connectionType == 2 {
			connectionType = 3
		} else if connectionType == 3 {
			connectionType = 2
		}
	}
	setIndex(a.connectionTypeCB, connectionType)
	a.portCB.SetText(config.Load("Port", "COM1"))
	a.tcpHostLE.SetText(config.Load("TCPHost", "127.0.0.1"))
	a.tcpPortLE.SetText(config.Load("TCPPort", "8080"))
	a.tcpLocalHostLE.SetText(config.Load("TCPLocalHost", "0.0.0.0"))
	a.tcpLocalPortLE.SetText(config.Load("TCPLocalPort", "8080"))
	a.tcpBindLocalCB.SetChecked(config.LoadInt("TCPBindLocal", 0) != 0)
	a.udpLocalHostLE.SetText(config.Load("UDPLocalHost", "0.0.0.0"))
	a.udpLocalPortLE.SetText(config.Load("UDPLocalPort", "8080"))
	a.udpRemoteHostLE.SetText(config.Load("UDPRemoteHost", "127.0.0.1"))
	a.udpRemotePortLE.SetText(config.Load("UDPRemotePort", "8080"))
	a.udpBindLocalCB.SetChecked(config.LoadInt("UDPBindLocal", 0) != 0)
	a.tcpServerHostLE.SetText(config.Load("TCPServerHost", "0.0.0.0"))
	a.tcpServerPortLE.SetText(config.Load("TCPServerPort", "8080"))
	a.baudCB.SetText(config.Load("Baud", "115200"))
	setIndex(a.dataCB, config.LoadInt("DataBits", 3))
	setIndex(a.parityCB, config.LoadInt("Parity", 0))
	setIndex(a.stopCB, config.LoadInt("StopBits", 0))
	setIndex(a.flowCB, config.LoadInt("Flow", 0))
	setIndex(a.encodingCB, config.LoadInt("Encoding", 0))
	setIndex(a.sendEncodingCB, config.LoadInt("SendEncoding", 0))
	setIndex(a.lineEndingCB, config.LoadInt("LineEnding", 0))
	a.intervalLE.SetText(config.Load("Interval", "1000"))
	a.dtrCB.SetChecked(config.LoadInt("DTR", 0) != 0)
	a.rtsCB.SetChecked(config.LoadInt("RTS", 0) != 0)
	a.autoReconnectCB.SetChecked(config.LoadInt("AutoReconnect", 1) != 0)
	a.markSenderCB.SetChecked(config.LoadInt("MarkSender", 0) != 0)
	a.escapeCB.SetChecked(config.LoadInt("Escape", 0) != 0)
	a.wrapRXCB.SetChecked(config.LoadInt("WrapRX", 1) != 0)
	a.wrapTXCB.SetChecked(config.LoadInt("WrapTX", 1) != 0)
	a.updateEscapeState()
	a.updateReceiveWrap()
	a.updateSendWrap()
	a.termModeCB.SetChecked(config.LoadInt("TermMode", 0) != 0)
	a.connectionTypeChanged()
}
func (a *app) saveSettings() {
	config.SaveInt("ConnectionType", a.connectionTypeCB.CurrentIndex())
	config.SaveInt("ConnectionTypeOrder", 1)
	config.Save("Port", a.portCB.Text())
	config.Save("TCPHost", a.tcpHostLE.Text())
	config.Save("TCPPort", a.tcpPortLE.Text())
	config.Save("TCPLocalHost", a.tcpLocalHostLE.Text())
	config.Save("TCPLocalPort", a.tcpLocalPortLE.Text())
	saveBool("TCPBindLocal", a.tcpBindLocalCB.Checked())
	config.Save("UDPLocalHost", a.udpLocalHostLE.Text())
	config.Save("UDPLocalPort", a.udpLocalPortLE.Text())
	config.Save("UDPRemoteHost", a.udpRemoteHostLE.Text())
	config.Save("UDPRemotePort", a.udpRemotePortLE.Text())
	saveBool("UDPBindLocal", a.udpBindLocalCB.Checked())
	config.Save("TCPServerHost", a.tcpServerHostLE.Text())
	config.Save("TCPServerPort", a.tcpServerPortLE.Text())
	config.Save("Baud", a.baudCB.Text())
	config.SaveInt("DataBits", a.dataCB.CurrentIndex())
	config.SaveInt("Parity", a.parityCB.CurrentIndex())
	config.SaveInt("StopBits", a.stopCB.CurrentIndex())
	config.SaveInt("Flow", a.flowCB.CurrentIndex())
	config.SaveInt("Encoding", a.encodingCB.CurrentIndex())
	config.SaveInt("SendEncoding", a.sendEncodingCB.CurrentIndex())
	config.SaveInt("LineEnding", a.lineEndingCB.CurrentIndex())
	config.Save("Interval", a.intervalLE.Text())
	saveBool("DTR", a.dtrCB.Checked())
	saveBool("RTS", a.rtsCB.Checked())
	saveBool("AutoReconnect", a.autoReconnectCB.Checked())
	saveBool("MarkSender", a.markSenderCB.Checked())
	saveBool("Escape", a.escapeCB.Checked())
	saveBool("WrapRX", a.wrapRXCB.Checked())
	saveBool("WrapTX", a.wrapTXCB.Checked())
	saveBool("TermMode", a.termModeCB.Checked())
}
func setIndex(cb *walk.ComboBox, index int) {
	if index < 0 {
		index = 0
	}
	cb.SetCurrentIndex(index)
}
func saveBool(name string, value bool) {
	if value {
		config.SaveInt(name, 1)
	} else {
		config.SaveInt(name, 0)
	}
}
