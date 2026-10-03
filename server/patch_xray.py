with open("/root/build/xray-src/app/proxyman/inbound/always.go", "r", encoding="utf-8") as f:
    always = f.read()
target = "errs = append(errs, h.mux.Close())"
repl = "errs = append(errs, h.mux.Close())\n\tif c, ok := h.proxy.(common.Closable); ok {\n\t\terrs = append(errs, c.Close())\n\t}"
if target in always and "h.proxy.(common.Closable)" not in always:
    always = always.replace(target, repl, 1)
    with open("/root/build/xray-src/app/proxyman/inbound/always.go", "w", encoding="utf-8") as f:
        f.write(always)
    print("always.go patched")
else:
    print("always.go: already patched or target not found")

with open("/root/build/xray-src/proxy/tun/handler.go", "r", encoding="utf-8") as f:
    handler = f.read()
target2 = "func (t *Handler) Process(ctx context.Context, network net.Network, conn stat.Connection, dispatcher routing.Dispatcher) error {\n\treturn nil\n}"
repl2 = target2 + "\n\nfunc (t *Handler) Close() error {\n\tif t.stack != nil {\n\t\terr := t.stack.Close()\n\t\tt.stack = nil\n\t\treturn err\n\t}\n\treturn nil\n}"
if target2 in handler and "func (t *Handler) Close()" not in handler:
    handler = handler.replace(target2, repl2, 1)
    with open("/root/build/xray-src/proxy/tun/handler.go", "w", encoding="utf-8") as f:
        f.write(handler)
    print("handler.go patched")
else:
    print("handler.go: already patched or target not found")
