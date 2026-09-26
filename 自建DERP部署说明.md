# 自建 Tailscale DERP 中继服务器部署说明

> 内容整理自：
- [部署 Tailscale Derper 自建中继服务器 - 猫猫博客](https://catcat.blog/2025/12/deploy-tailscale-derper)
- [自建免备案防偷 Tailscale 国内中继（DERP）教程](https://blog.sleepstars.net/archives/ji-yu-docker-compose)

Tailscale 是一个便捷的组网工具，可以将不同网络环境、甚至不同国家地区的设备连接到同一个虚拟局域网中。当 P2P 打洞失败时，流量会通过 Tailscale 的 DERP（Designated Encrypted Relay for Packets）中继节点进行转发。

由于官方的 DERP 服务器主要分布在国外，在某些网络环境下中转延迟会非常高，严重影响使用体验。通过自建 DERP 服务器，可以显著降低中转延迟，提高访问速度。

## 目录

- [准备工作](#准备工作)
- [方法一：Docker 部署（推荐）](#方法一docker-部署推荐)
- [方法二：手动编译部署](#方法二手动编译部署)
- [证书管理](#证书管理)
- [客户端验证](#客户端验证)
- [配置 Tailscale](#配置-tailscale)
- [测试验证](#测试验证)
- [常见问题](#常见问题)
- [证书更新脚本与定时任务](#证书更新脚本与定时任务)
- [总结](#总结)

## 准备工作

在开始部署前，你需要准备：

- **服务器**：一台具有公网 IP 的服务器（建议选择离你的使用地区最近的服务器）
- **域名**：一个解析到服务器的域名（如 `derper.example.com`）
- **防火墙配置**：开放以下端口
  - TCP 80（HTTP，用于证书验证）
  - TCP 443（HTTPS，DERP 服务）
  - UDP 3478（STUN 打洞）

> **注意**：如果使用中国大陆的服务器，域名需要完成 ICP 备案才能使用 80 和 443 端口。

## 方法一：Docker 部署（推荐）

Docker 部署方式更加简单快捷，适合大多数用户。我们使用 [fredliang/derper](https://github.com/fredliang44/derper-docker) 镜像，这是一个已经打包好的 Derper Docker 镜像。

### 安装 Docker

如果你的服务器还没有安装 Docker 和 Docker Compose，请先安装：

```bash
# 安装 Docker（以 Ubuntu 为例）
curl -fsSL https://get.docker.com | bash

# 启动 Docker 服务
sudo systemctl enable docker
sudo systemctl start docker
```

### 创建 Docker Compose 配置

创建一个工作目录并编写 `docker-compose.yml`：

```bash
mkdir -p /opt/derper
cd /opt/derper
```

创建 `docker-compose.yml` 文件：

```yaml
version: '3.8'
services:
  derper:
    image: fredliang/derper
    container_name: derper
    restart: always
    environment:
      - DERP_CERT_MODE=manual  # 使用手动证书
      - DERP_ADDR=:443  # DERP 服务端口
      - DERP_HTTP_PORT=80  # HTTP 端口（用于证书验证）
      - DERP_STUN_PORT=3478  # STUN 端口
      - DERP_DOMAIN=derper.example.com  # 替换为你的域名
      - DERP_VERIFY_CLIENTS=false  # 启用客户端验证（tailcat特殊性服务端没有tailscale，因此设置false）
    ports:
      - "443:443"
      - "80:80"
      - "3478:3478/udp"
    volumes:
      - /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock
      - ./certs:/app/certs
```

### 环境变量说明

| 变量名 | 必需 | 说明 | 默认值 |
| --- | --- | --- | --- |
| `DERP_DOMAIN` | 是 | DERP 服务器的域名 | `your-hostname.com` |
| `DERP_CERT_DIR` | 否 | 证书存放目录 | `/app/certs` |
| `DERP_CERT_MODE` | 否 | 证书模式：`manual`（手动）或 `letsencrypt`（自动申请） | `letsencrypt` |
| `DERP_ADDR` | 否 | DERP 服务监听地址 | `:443` |
| `DERP_STUN` | 否 | 是否启用 STUN 服务 | `true` |
| `DERP_STUN_PORT` | 否 | STUN 服务端口 | `3478` |
| `DERP_HTTP_PORT` | 否 | HTTP 服务端口，设置为 `-1` 可禁用 | `80` |
| `DERP_VERIFY_CLIENTS` | 否 | 通过本地 Tailscale 客户端验证连接者身份 | `false` |

### 使用自定义端口

如果你的服务器 80/443 端口已被占用，可以使用自定义端口：

```yaml
version: '3.8'
services:
  derper:
    image: fredliang/derper
    container_name: derper
    restart: always
    environment:
      - DERP_CERT_MODE=manual
      - DERP_ADDR=:13477  # 自定义 DERP 端口
      - DERP_HTTP_PORT=13476  # 自定义 HTTP 端口
      - DERP_STUN_PORT=13478  # 自定义 STUN 端口
      - DERP_DOMAIN=derper.example.com
      - DERP_VERIFY_CLIENTS=false  # 启用客户端验证（tailcat特殊性服务端没有tailscale，因此设置false）
    ports:
      - "13477:13477"
      - "13476:13476"
      - "13478:13478/udp"
    volumes:
      - /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock
      - ./certs:/app/certs
```

> **注意**：使用自定义端口时，后续在 Tailscale ACL 中（[JSON editor - Tailscale](https://console.tailscale.com/admin/acls/file)）配置 `DERPPort` 和 `STUNPort` 需要对应修改。

## 方法二：手动编译部署

如果你希望更深入地了解 Derper 的运行机制，或者需要定制化编译，可以选择手动编译方式。

### 安装 Golang

Derper 是用 Go 语言编写的，需要先安装 Golang（建议 1.21 或更高版本）：

```bash
# 下载 Golang（以 1.22.0 为例）
wget https://go.dev/dl/go1.22.0.linux-amd64.tar.gz

# 解压到 /usr/local
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.22.0.linux-amd64.tar.gz

# 配置环境变量
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# 验证安装
go version
```

### 编译 Derper

```bash
# 克隆 Tailscale 仓库
git clone https://github.com/tailscale/tailscale.git
cd tailscale

# 编译 derper
go build cmd/derper/derper.go

# 移动到系统路径
sudo mv derper /usr/sbin/derper
```

### 创建 Systemd 服务

创建服务文件 `/etc/systemd/system/derper.service`：

```ini
[Unit]
Description=Tailscale Derper
Wants=network-pre.target
After=network-pre.target NetworkManager.service systemd-resolved.service

[Service]
ExecStart=/usr/sbin/derper \
  --hostname=derper.example.com \
  -a :443 \
  -http-port 80 \
  -certmode letsencrypt \
  --certdir /var/lib/derper/certs \
  -verify-clients
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

参数说明：

- `--hostname`：你的域名
- `-a`：DERP 服务监听地址
- `-http-port`：HTTP 端口，用于 Let's Encrypt 证书验证
- `-certmode`：证书模式，`letsencrypt` 为自动申请，`manual` 为手动管理
- `--certdir`：证书存放目录
- `-verify-clients`：启用客户端验证（防止被白嫖）

### 启动服务

```bash
# 创建证书目录
sudo mkdir -p /var/lib/derper/certs

# 重载 systemd 配置
sudo systemctl daemon-reload

# 启动并设置开机自启
sudo systemctl enable derper
sudo systemctl start derper

# 查看服务状态
sudo systemctl status derper
```

## 证书管理

### 自动申请证书（Let's Encrypt）

如果使用标准的 80/443 端口，Derper 可以自动申请和续期 Let's Encrypt 证书：

**Docker 方式：**

```yaml
environment:
  - DERP_CERT_MODE=letsencrypt
  - DERP_ADDR=:443
  - DERP_HTTP_PORT=80
```

**手动编译方式：**

```ini
ExecStart=/usr/sbin/derper \
  --hostname=derper.example.com \
  -a :443 \
  -http-port 80 \
  -certmode letsencrypt \
  --certdir /var/lib/derper/certs
```

### 手动管理证书

如果使用自定义端口或已有证书，需要手动管理证书。

证书文件命名规则：

- `域名.crt`（完整证书链）
- `域名.key`（私钥）

**例如：`derper.example.com.crt` 和 `derper.example.com.key`，名称必须与DERP_DOMAIN中设置域名一致。**

将证书放置在：

- Docker：`./certs/` 目录（与 docker-compose.yml 同级）
- 手动编译：`/var/lib/derper/certs/` 目录

## 客户端验证

为了防止自建的 Derper 服务器被其他人白嫖，强烈建议启用客户端验证功能。启用后，只有你的 Tailscale 网络中的设备才能使用该 Derper 服务器。

### 安装 Tailscale 客户端

在 Derper 服务器上安装 Tailscale 客户端：

```bash
# 安装 Tailscale
curl -fsSL https://tailscale.com/install.sh | sh

# 启动并加入网络
sudo tailscale up
```

### 启用验证

**Docker 方式：**

确保 `docker-compose.yml` 中包含：

```yaml
environment:
  - DERP_VERIFY_CLIENTS=true
volumes:
  - /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock
```

**手动编译方式：**

在 systemd 服务文件中添加 `-verify-clients` 参数：

```ini
ExecStart=/usr/sbin/derper \
  --hostname=derper.example.com \
  -a :443 \
  -http-port 80 \
  -certmode letsencrypt \
  --certdir /var/lib/derper/certs \
  -verify-clients
```

重启服务：

```bash
sudo systemctl daemon-reload
sudo systemctl restart derper
```

## 配置 Tailscale

### 修改 Access Controls

1. 访问 [Tailscale Admin Console](https://login.tailscale.com/admin/acls/file)
2. 在 `Access Controls` 中添加自定义 DERP 配置

在配置文件末尾添加（保留其他配置不变）：

```jsonc
{
  // ... 其他配置 ...

  "derpMap": {
    "OmitDefaultRegions": false,  // false为保留官方服务器作为备用，如不需要官方服务器则改为true
    "Regions": {
      "900": {
        "RegionID": 900,
        "RegionCode": "myderp",
        "RegionName": "My Custom Derper",
        "Nodes": [
          {
            "Name": "1",
            "RegionID": 900,
            "HostName": "derper.example.com",  // 你的域名
            "IPv4": "1.1.1.1", // 防止DNS解析无法找到
            "DERPPort": 443,  // 如使用自定义端口，修改此处
            "STUNPort": 3478,  // 如使用自定义端口，修改此处
            "STUNOnly": false
          }
        ]
      }
    }
  }
}
```

配置说明：

- `RegionID`：自定义 ID，建议使用 900+ 避免与官方冲突
- `RegionCode`：区域代码，可自定义
- `RegionName`：显示名称，可使用中文
- `HostName`：Derper 服务器域名
- `IPv4`：Derper 所在服务器公网IP地址
- `DERPPort`：DERP 服务端口（默认 443）
- `STUNPort`：STUN 服务端口（默认 3478）
- `OmitDefaultRegions`：是否禁用官方服务器（建议保留作为备用）

### 禁用官方 DERP 服务器（可选）

如果你希望只使用自建服务器，可以禁用官方 DERP：

```jsonc
{
  "derpMap": {
    "OmitDefaultRegions": false,
    "Regions": {
      "900": {
        // ... 你的自建服务器配置 ...
      },
      // 禁用特定官方服务器
      "1": null,
      "2": null,
      "3": null,
      // ... 其他 ID ...
      // 建议保留一个作为备用
      // "20": null,  # Hong Kong，作为备选
    }
  }
}
```

> **警告**：不建议完全禁用所有官方服务器，以防自建服务器故障时 Tailscale 完全不可用。

## 测试验证

配置完成后，在任意已连接 Tailscale 的客户端上测试：

### 检查网络状态

```bash
tailscale netcheck
```

你应该看到类似输出：

```
Report:
  * UDP: true
  * IPv4: yes, 1.2.3.4:41234
  * IPv6: yes, [2001:db8::1]:41234
  * MappingVariesByDestIP: false
  * PortMapping: UPnP, NAT-PMP
  * CaptivePortal: false
  * Nearest DERP: My Custom Derper
  * DERP latency:
    - myderp:  15ms  (My Custom Derper)
    - tok:     85ms  (Tokyo)
    - hkg:     42ms  (Hong Kong)
```

`Nearest DERP` 显示为你的自建服务器名称，说明配置成功。

### 测试连接延迟

```bash
tailscale ping <设备名称或IP>
tailscale netcheck
```

```
Report:
        * Time: 2025-12-30T02:39:55.29863832Z
        * UDP: true
        * IPv4: yes, ip:port
        * IPv6: no, but OS has support
        * MappingVariesByDestIP: false
        * PortMapping:
        * CaptivePortal: false
        * Nearest DERP: catcat-derp
        * DERP latency:
                -  sa: 43.2ms  (catcat-derp)
                - lax: 155.9ms (Los Angeles)
                - sea: 165.5ms (Seattle)
                - den: 169.7ms (Denver)
                - sfo: 172.9ms (San Francisco)
                - nue: 173.2ms (Nuremberg)
                - dfw: 177.9ms (Dallas)
                - hel: 182.8ms (Helsinki)
                - hkg: 184.2ms (Hong Kong)
                - hnl: 192.1ms (Honolulu)
                - iad: 194.7ms (Ashburn)
                - tor: 203.8ms (Toronto)
                - ord: 206.5ms (Chicago)
                - nyc: 207.6ms (New York City)
                - mia: 216.1ms (Miami)
                - tok: 216.2ms (Tokyo)
                - par: 220.2ms (Paris)
                - fra: 220.7ms (Frankfurt)
                - lhr: 225.3ms (London)
                - ams: 231.4ms (Amsterdam)
                - waw: 237.6ms (Warsaw)
                - mad: 238.4ms (Madrid)
                - sin: 248.2ms (Singapore)
                - syd: 291.3ms (Sydney)
                - sao: 335.8ms (São Paulo)
                - dbi: 340ms   (Dubai)
                - blr: 344.4ms (Bangalore)
                - nai: 359.3ms (Nairobi)
                - jnb: 385.1ms (Johannesburg)
```

观察自建 DERP 名称（如 `catcat-derp`，取决于你的命名）字样，表示流量正在通过你的自建 Derper 中转。

### 检查服务器日志

**Docker 方式：**

```bash
docker logs -f derper
```

**手动编译方式：**

```bash
sudo journalctl -u derper -f
```

成功的连接日志类似：

```
derper: accepting connection from 100.64.1.2:41234
derper: serving client 100.64.1.2
```

## 常见问题

### 1. 证书验证失败

**问题**：Let's Encrypt 证书申请失败

**解决方法**：

- 确认域名已正确解析到服务器 IP
- 确认防火墙已开放 80 端口
- 检查是否有其他服务占用 80 端口
- 查看服务日志排查具体错误

### 2. 客户端无法连接

**问题**：`tailscale netcheck` 看不到自建服务器

**解决方法**：

- 确认 Tailscale ACL 配置已保存
- 等待几分钟让配置生效
- 重启客户端 Tailscale 服务
- 检查服务器防火墙是否正确开放端口

### 3. DERP 验证失败

**问题**：启用 `verify-clients` 后无法连接

**解决方法**：

- 确认服务器上 Tailscale 客户端已启动并加入网络
- 检查 `/var/run/tailscale/tailscaled.sock` 是否存在
- Docker 方式确认 socket 文件已正确挂载
- 查看日志确认验证流程

### 4. 使用自定义端口后无法连接

**问题**：修改端口后服务不可用

**解决方法**：

- 确认防火墙已开放对应端口
- 确认 ACL 中 `DERPPort` 和 `STUNPort` 已对应修改
- 使用 `telnet` 或 `nc` 测试端口连通性

## 证书更新脚本与定时任务

如果证书是手动从其他目录复制过来的（例如由 acme.sh / 宝塔 / 其他服务续期后生成），可以用仓库自带的 `update-derp-certs.sh` 脚本完成复制、重命名、校验和重启容器，再配合 crontab 定时执行，实现准自动化更新。

### 脚本功能

`update-derp-certs.sh` 按顺序做这几件事：

1. **校验**：解析源证书与私钥，确认二者匹配，并输出有效期
2. **备份**：旧证书只保留最近一次被替换的副本（`*.bak`，每次覆盖）
3. **复制并重命名**：写入临时文件后原子替换，避免中断留下半截文件
   - 源：`/home/ubuntu/ssl_certs/flashatom.online.crt` / `.key`
   - 目标：`/home/ubuntu/tailscale_derp/certs/derper.flashatom.online.crt` / `.key`（文件名必须与 `DERP_DOMAIN` 一致）
   - 权限：证书 644、私钥 600
4. **重启容器**：优先 `docker compose restart`，找不到 compose 文件则 `docker restart derper`；Docker 命令无权限时自动改用 `sudo docker`

脚本默认路径假设：

| 路径 | 用途 |
| --- | --- |
| `/home/ubuntu/ssl_certs/` | 证书源目录 |
| `/home/ubuntu/tailscale_derp/` | docker-compose 所在目录 |
| `/home/ubuntu/tailscale_derp/certs/` | DERP 证书目录 |
| `/home/ubuntu/tailscale_derp/update-derp-certs.sh` | 脚本本体 |
| `/home/ubuntu/tailscale_derp/log/` | 定时任务日志目录 |

路径和源文件名均可用环境变量覆盖，例如：

```bash
SRC_CERT=fullchain.pem SRC_KEY=privkey.pem ./update-derp-certs.sh
```

手动执行一次验证：

```bash
cd /home/ubuntu/tailscale_derp
./update-derp-certs.sh
```

看到 `完成。可用 docker logs -f derper 确认服务正常。` 即成功。

### 配置 Docker 免密（cron 必需）

cron 环境下如果 Docker 需要 sudo 密码会卡住，必须先配置免密。推荐把用户加入 `docker` 组（之后完全不需要 sudo）：

```bash
sudo usermod -aG docker ubuntu
# 重新登录生效，或当前会话立即生效：
newgrp docker
```

验证：

```bash
docker ps   # 不需要 sudo 即可执行
```

> **注意**：加入 `docker` 组等同于 root 权限（可挂载宿主机目录、起特权容器）。个人服务器无妨，多用户环境需评估风险。

备选方案——只给 docker 命令配免密 sudo：

```bash
echo 'ubuntu ALL=(root) NOPASSWD: /usr/bin/docker' | sudo tee /etc/sudoers.d/docker
sudo chmod 440 /etc/sudoers.d/docker
sudo visudo -c   # 确认语法无误
```

验证：

```bash
sudo -n docker ps   # -n 表示不提示密码
```

### 定时任务（每月 5 号 02:00）

```bash
crontab -e
```

添加以下一行：

```bash
0 2 5 * * mkdir -p /home/ubuntu/tailscale_derp/log && /home/ubuntu/tailscale_derp/update-derp-certs.sh >> /home/ubuntu/tailscale_derp/log/update-derp-certs-$(date +\%Y\%m\%d).log 2>&1; find /home/ubuntu/tailscale_derp/log -name 'update-derp-certs-*.log' -mtime +183 -delete
```

说明：

- `0 2 5 * *`：每月 5 号 02:00 执行
- 日志按日期命名：`/home/ubuntu/tailscale_derp/log/update-derp-certs-YYYYMMDD.log`
- `find -mtime +183 -delete`：只保留约 6 个月内的日志
- crontab 中 `%` 是特殊字符，必须写成 `\%`（上面已处理）
- 开头 `mkdir -p` 保证 `log/` 目录存在

查看当前用户的定时任务：

```bash
crontab -l
```

查看执行日志：

```bash
ls -lt /home/ubuntu/tailscale_derp/log/
```

## 总结

通过自建 Tailscale Derper 服务器，可以有效降低中转延迟，提升网络体验。Docker 部署方式简单快捷，适合快速上手；手动编译方式则提供了更多的灵活性和控制。

建议生产环境中启用客户端验证和自动证书续期，确保服务的安全性和稳定性。同时保留部分官方 DERP 服务器作为备用，避免单点故障导致网络不可用。

---

**参考来源**：

- [部署 Tailscale Derper 自建中继服务器](https://catcat.blog/2025/12/deploy-tailscale-derper)（作者：猫猫博客，许可协议：CC BY-NC-SA 4.0）
- [自建免备案防偷 Tailscale 国内中继（DERP）教程](https://blog.sleepstars.net/archives/ji-yu-docker-compose)
