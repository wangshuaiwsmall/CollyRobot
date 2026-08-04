# CollyRobot Web Host

该程序将 `dist` 嵌入 Go 二进制，并把浏览器的 `/api/*` 请求代理到 Gin 后端。运行环境无需 Node.js 或 pnpm。

在具备 Node.js、pnpm 与 Go 的构建环境执行：

```bat
scripts\build-webhost.cmd
```

产物为 `bin\collyrobot-webhost.exe`。部署时同时启动后端与该程序：

```bat
# 后端 API，默认监听 8080
.\collyrobot-server.exe

# 前端站点，默认监听 8081，并将 /api 代理到 8080
.\collyrobot-webhost.exe
```

可选环境变量：

- `WEB_HOST_ADDR`：站点监听地址，默认 `:8081`
- `BACKEND_URL`：后端 API 地址，默认 `http://127.0.0.1:8080`

也可通过命令行覆盖：

```bat
.\collyrobot-webhost.exe -addr :80 -backend http://127.0.0.1:8080
```
