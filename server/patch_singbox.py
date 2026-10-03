path = "/root/build/sing-box-src/protocol/tun/inbound.go"
content = open(path, "r", encoding="utf-8").read()
target = '\t\tmonitor.Start("open interface")\n\t\tif t.platformInterface != nil {'
replacement = '\t\tmonitor.Start("open interface")\n\t\tif envFd := os.Getenv("SING_BOX_TUN_FD"); envFd != "" {\n\t\t\tif fd, err := strconv.Atoi(envFd); err == nil && fd > 0 {\n\t\t\t\ttunOptions.FileDescriptor = fd\n\t\t\t}\n\t\t}\n\t\tif t.platformInterface != nil {'

if target in content:
    content = content.replace(target, replacement, 1)
    open(path, "w", encoding="utf-8").write(content)
    print("Patched sing-box inbound.go successfully!")
else:
    print("Already patched or target not found")
