// +build windows

package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"unicom/internal/codec"
	"unicom/internal/config"
	"unicom/internal/connection"
	"unicom/internal/serialwin"
	"unicom/internal/session"
)

const appTitle = "UniCom 串口调试助手"

type app struct {
	mw                                                                *walk.MainWindow
	portCB, baudCB, dataCB, parityCB, stopCB, flowCB                  *walk.ComboBox
	encodingCB, sendEncodingCB, lineEndingCB                          *walk.ComboBox
	openBtn, refreshBtn, sendBtn, clearBtn, saveBtn                   *walk.PushButton
	dtrCB, rtsCB, autoReconnectCB, hexRXCB, timestampCB, autoScrollCB *walk.CheckBox
	hexTXCB, cycleCB                                                  *walk.CheckBox
	receiveTE, sendTE                                                 *walk.TextEdit
	intervalLE                                                        *walk.LineEdit
	statusItem, countersItem                                          *walk.StatusBarItem

	manager            *connection.Manager
	buffer             *session.Buffer
	mu                 sync.Mutex
	connected, opening bool
	txBytes            uint64
	lastShownRX        uint64
	lastRender         time.Time
	pending            []byte
	renderEncoding     string
	cycleStop          chan struct{}
}

var encodings = []string{"UTF-8", "GBK", "GB18030", "Big5", "ASCII", "UTF-16LE", "UTF-16BE"}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	a := &app{buffer: session.NewBuffer(2 * 1024 * 1024)}
	if err := a.run(); err != nil {
		walk.MsgBox(nil, appTitle, err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
	}
}

func (a *app) run() error {
	if err := a.createUI(); err != nil {
		return err
	}
	a.manager = connection.New(a.onData, a.onConnectionEvent, a.onTX)
	a.loadSettings()
	a.refreshPorts()
	a.updateControls()
	a.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) { a.stopCycle(); a.manager.Close(); a.saveSettings() })
	go a.periodicUI()
	a.mw.Show()
	a.mw.Run()
	return nil
}

func (a *app) createUI() error {
	return MainWindow{
		AssignTo: &a.mw, Title: appTitle, MinSize: Size{Width: 840, Height: 580}, Size: Size{Width: 1040, Height: 720},
		Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}, Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 6},
		StatusBarItems: []StatusBarItem{{AssignTo: &a.statusItem, Text: "串口已关闭", Width: 620}, {AssignTo: &a.countersItem, Text: "RX 0 B   TX 0 B", Width: 260}},
		Children: []Widget{
			Composite{Layout: HBox{MarginsZero: true, Spacing: 5}, Children: []Widget{
				Label{Text: "串口"}, ComboBox{AssignTo: &a.portCB, Editable: true, Model: []string{}}, PushButton{AssignTo: &a.refreshBtn, Text: "刷新", OnClicked: a.refreshPorts},
				Label{Text: "波特率"}, ComboBox{AssignTo: &a.baudCB, Editable: true, Model: []string{"1200", "2400", "4800", "9600", "19200", "38400", "57600", "115200", "230400", "460800", "921600"}},
				Label{Text: "数据位"}, ComboBox{AssignTo: &a.dataCB, Model: []string{"5", "6", "7", "8"}},
				Label{Text: "校验"}, ComboBox{AssignTo: &a.parityCB, Model: []string{"无", "奇", "偶", "Mark", "Space"}},
				Label{Text: "停止位"}, ComboBox{AssignTo: &a.stopCB, Model: []string{"1", "1.5", "2"}},
				PushButton{AssignTo: &a.openBtn, Text: "打开串口", MinSize: Size{Width: 82}, OnClicked: a.togglePort},
			}},
			Composite{Layout: HBox{MarginsZero: true, Spacing: 8}, Children: []Widget{
				Label{Text: "流控"}, ComboBox{AssignTo: &a.flowCB, Model: []string{"无", "RTS/CTS", "XON/XOFF"}},
				CheckBox{AssignTo: &a.dtrCB, Text: "DTR"}, CheckBox{AssignTo: &a.rtsCB, Text: "RTS"}, CheckBox{AssignTo: &a.autoReconnectCB, Text: "断线自动重连", Checked: true}, HSpacer{},
				Label{Text: "接收"}, CheckBox{AssignTo: &a.hexRXCB, Text: "HEX", OnCheckedChanged: a.renderAll}, Label{Text: "编码"}, ComboBox{AssignTo: &a.encodingCB, Model: encodings, OnCurrentIndexChanged: a.renderAll},
				CheckBox{AssignTo: &a.timestampCB, Text: "时间戳", OnCheckedChanged: a.renderAll}, CheckBox{AssignTo: &a.autoScrollCB, Text: "自动滚动", Checked: true},
				PushButton{AssignTo: &a.clearBtn, Text: "清空", OnClicked: a.clearReceive}, PushButton{AssignTo: &a.saveBtn, Text: "保存", OnClicked: a.saveReceive},
			}},
			TextEdit{AssignTo: &a.receiveTE, ReadOnly: true, VScroll: true, HScroll: true, MaxLength: 4 * 1024 * 1024, Font: Font{Family: "Consolas", PointSize: 10}},
			Composite{Layout: VBox{MarginsZero: true, Spacing: 5}, Children: []Widget{
				TextEdit{AssignTo: &a.sendTE, MinSize: Size{Height: 78}, VScroll: true, HScroll: true, Font: Font{Family: "Consolas", PointSize: 10}},
				Composite{Layout: HBox{MarginsZero: true, Spacing: 7}, Children: []Widget{
					CheckBox{AssignTo: &a.hexTXCB, Text: "HEX 发送"}, Label{Text: "编码"}, ComboBox{AssignTo: &a.sendEncodingCB, Model: encodings},
					Label{Text: "行尾"}, ComboBox{AssignTo: &a.lineEndingCB, Model: []string{"无", "CR", "LF", "CRLF"}},
					CheckBox{AssignTo: &a.cycleCB, Text: "周期发送", OnCheckedChanged: a.cycleChanged}, LineEdit{AssignTo: &a.intervalLE, Text: "1000", MaxLength: 7, MinSize: Size{Width: 70}}, Label{Text: "ms"}, HSpacer{},
					PushButton{AssignTo: &a.sendBtn, Text: "发送", MinSize: Size{Width: 92}, OnClicked: a.send},
				}},
			}},
		},
	}.Create()
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
	c, err := a.serialConfig()
	if err != nil {
		a.showError(err)
		return
	}
	a.mu.Lock()
	a.opening = true
	a.mu.Unlock()
	a.updateControls()
	a.manager.Open(c)
}

func (a *app) onConnectionEvent(e connection.Event) {
	a.mw.Synchronize(func() {
		if e.State == connection.StateWaiting && !a.autoReconnectCB.Checked() {
			go a.manager.Close()
			return
		}
		a.mu.Lock()
		a.connected = e.State == connection.StateConnected
		a.opening = e.State != connection.StateClosed
		a.mu.Unlock()
		a.statusItem.SetText(e.Message)
		a.updateControls()
	})
}

func (a *app) onData(p []byte) { a.buffer.Append(p); a.scheduleRender() }
func (a *app) onTX(n int)      { a.mu.Lock(); a.txBytes += uint64(n); a.mu.Unlock() }
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

func (a *app) renderAll() {
	data, rx := a.buffer.Snapshot()
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
	a.receiveTE.SetText(text)
	if a.autoScrollCB.Checked() {
		a.receiveTE.SetTextSelection(a.receiveTE.TextLength(), a.receiveTE.TextLength())
	}
	a.lastShownRX = rx
	a.updateCounters(rx)
}

func (a *app) renderPending() {
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
		a.receiveTE.AppendText(text)
		a.lastShownRX = rx
		a.updateCounters(rx)
		if a.autoScrollCB.Checked() {
			a.receiveTE.SetTextSelection(a.receiveTE.TextLength(), a.receiveTE.TextLength())
		}
		return
	}
	data = append(a.pending, data...)
	n := codec.CompletePrefix(data, a.selectedEncoding())
	a.pending = append(a.pending[:0], data[n:]...)
	if n > 0 {
		a.receiveTE.AppendText(codec.Decode(data[:n], a.selectedEncoding()))
	}
	if a.autoScrollCB.Checked() {
		a.receiveTE.SetTextSelection(a.receiveTE.TextLength(), a.receiveTE.TextLength())
	}
	a.lastShownRX = rx
	a.updateCounters(rx)
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
	b, err := codec.Encode(a.sendTE.Text(), a.sendEncodingCB.Text())
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
func (a *app) clearReceive() {
	a.buffer.Clear()
	a.pending = a.pending[:0]
	a.receiveTE.SetText("")
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
	a.openBtn.SetText(map[bool]string{true: "关闭串口", false: "打开串口"}[opening])
	a.sendBtn.SetEnabled(connected)
	a.refreshBtn.SetEnabled(!opening)
	for _, w := range []walk.Widget{a.portCB, a.baudCB, a.dataCB, a.parityCB, a.stopCB, a.flowCB, a.dtrCB, a.rtsCB} {
		w.SetEnabled(!opening)
	}
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
	a.portCB.SetText(config.Load("Port", "COM1"))
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
}
func (a *app) saveSettings() {
	config.Save("Port", a.portCB.Text())
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
