# UniCom 串口调试助手

UniCom 是使用 Go 和 Walk 编写的单文件 Windows 串口调试工具，发布版本以 Windows XP SP3 和 Windows 7 为兼容基线。

## 功能

- 常用串口参数及自定义波特率
- 文本或 HEX 收发
- UTF-8、GBK、GB18030、Big5、ASCII、UTF-16LE/BE
- 串口意外断开后自动重连
- 周期发送、CR/LF/CRLF 行尾
- 接收缓存、收发计数、自动滚动
- 接收内容保存为文本或原始二进制
- 用户配置保存在 `HKCU\Software\UniCom`

## 运行

直接运行 `build\UniCom-XP-x86.exe`。程序不需要安装，不依赖 .NET、VC++ 运行库或额外 DLL。

USB 转串口设备仍需要系统中安装对应厂商驱动。

## 构建

项目固定使用 Go 1.10.8、Walk 2018-08-27 和 Win 2018-08-21。依赖已经放在 `vendor` 中。

Git Bash：

```bash
./build.sh
```

跳过测试时可执行 `./build.sh --skip-tests`。

PowerShell：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\build.ps1
```

构建脚本只在 `build` 下创建临时 GOPATH，输出为 `build\UniCom-XP-x86.exe`。

## 兼容性说明

- 发布架构为 32 位，可在 32/64 位 Windows 上运行。
- 自动重连跟踪原 COM 端口号；重插后端口号改变时，需要重新选择端口。
- GB18030 使用系统代码页 54936。极少数裁剪版 XP 未安装该代码页时，程序会提示不支持。
