# Docker 构建与部署

提供本地构建与 GitHub Actions＋GHCR 发布配置，适用于个人使用、单实例部署。本地构建默认镜像名为 `ledgergate:local`，发布地址为 `ghcr.io/sectsnow/ledgergate`。

## 首次运行

安装可用的 Docker Engine 与 Docker Compose 后，在仓库根目录执行：

```bash
docker compose config --quiet
docker compose build --pull
docker compose up -d
docker compose ps
docker compose logs --tail=100 ledgergate
```

默认访问 `http://服务器IP:8081`，客户端 API 地址为 `http://服务器IP:8081/v1`。当前宿主机的 systemd 服务继续使用 8080，容器先使用 8081 验收。

全新数据卷首次访问时，通过网页设置管理员密码，无需在镜像或 Compose 中填写密码。页面提示设置成功后即可接入账户、配置转发渠道与模型。

容器固定监听 8080，宿主机端口可在启动前覆盖，例如：

```bash
LEDGERGATE_PORT=18080 docker compose up -d
```

修改端口后更新浏览器和客户端地址。上游或代理配置中的 `127.0.0.1` 指容器自身，应改为容器能够访问的地址。

程序默认监听 `0.0.0.0:8080`，镜像已设置数据目录 `/data` 和时区 `Asia/Shanghai`，Compose 无需重复设置；需要调整时，在服务中添加 `environment` 配置即可覆盖。

## GitHub Actions 与 GHCR

`.github/workflows/publish.yml` 使用 GitHub 托管的 Ubuntu 运行器构建，不要求开发机预装 Docker。工作流需要先随源码提交到仓库，才能由 GitHub 执行；仅在本地创建文件不会触发发布。

工作流有两个入口：

- 推送与 `web/package.json` 一致的 SemVer 版本标签，例如 `v0.1.6`，生成 `0.1.6`、`latest` 和 `sha-完整提交SHA` 镜像标签。预发布标签如 `v0.1.6-rc.1` 生成对应版本和 SHA 标签，不更新 `latest`。
- 在 Actions 中手动运行“发布 GHCR 镜像”。选择 `main` 分支时只生成 `sha-完整提交SHA`，适合首次候选构建，不更新 `latest`。

发布前先执行前端测试、类型检查与生产构建及 Go 全量测试，再构建 `linux/amd64` 镜像。在运行器的临时目录准备绑定目录，检查主进程 UID/GID 为 0、有效能力仅包含 `DAC_OVERRIDE` 和 `FOWNER`、健康状态、嵌入的前端资源和首次设置密码；停止后将数据所有者改为旧镜像使用的 UID/GID 10001，再强制重建，确认原密钥不变、原密码仍可登录，通过后才上传镜像。此检查使用临时空数据，不调用真实上游，也不替代实际账户采集、转发和备份恢复验收。

上传使用内置 `GITHUB_TOKEN`，权限限定为读取源码和写入 Packages，无需添加发布用 PAT。第三方 Actions 固定到已核对的提交 SHA。镜像名称自动按仓库路径转为小写；当前仓库对应 `ghcr.io/sectsnow/ledgergate`。

LedgerGate 的 GHCR 镜像包已公开，拉取镜像无需登录；源码仓库保持私有，镜像包的可见性单独设置。

在仓库根目录的 `.env` 文件中指定已发布的镜像，例如：

```dotenv
LEDGERGATE_IMAGE=ghcr.io/sectsnow/ledgergate:0.1.6
```

然后拉取并运行，复用同一份 Compose 的端口、数据卷和安全配置：

```bash
docker compose config --quiet
docker compose pull
docker compose up -d --no-build
docker compose ps
```

手动候选构建时，将 `.env` 中的标签换成对应的 `sha-完整提交SHA`；正式部署建议固定版本或镜像 digest，更新时先备份，再修改镜像地址并重新执行拉取与启动命令。使用 GHCR 镜像部署时，`--no-build` 可避免本地构建覆盖选定镜像。

## 镜像和数据

Dockerfile 使用三个阶段：Node 22 构建前端，Go 1.26 构建嵌入前端的静态程序，最终 Alpine 镜像保留程序、证书、时区和基础运行工具。镜像默认以容器内 root（UID/GID `0:0`）直接执行程序，镜像内 `/data` 的初始权限为 `0700`；绑定已有目录时保留其目录权限，程序负责创建缺失的目录和首次运行所需文件。已有目录不递归修改所有者，原有密钥、密码与数据库继续使用。

从 `0.1.1` 起默认使用 root；`0.1.0` 以 UID/GID `10001:10001` 运行，继续使用旧版时可在 Compose 中设置 `user: "0:0"`。升级需重新拉取镜像并重建容器；显式指定非 root `user` 时，需要自行匹配挂载目录的读写权限。

`.dockerignore` 默认排除所有文件，只允许构建需要的源码和公共资源；数据目录、私钥、环境变量文件、数据库与 `.lgb` / `.lgd` 备份再次明确排除。最终镜像只从后端构建阶段复制程序。

Compose 的持久化卷为 `ledgergate_data`，挂载到 `/data`，保存完整数据库、WAL/SHM、`master.key`、`admin-password` 及后续运行数据。重建容器或普通 `docker compose down` 不会删除这个命名卷。新建命名卷会继承镜像中 `/data` 的属主及权限；默认 root 的镜像也可读写旧版 UID 10001 所有的数据及普通 NAS 本地绑定目录。

`docker compose down -v` 会删除数据卷，不用于普通更新。切勿只备份 `.db` 而遗漏主密钥与密码文件，也不要让两个实例同时打开同一份数据。

容器根文件系统只读，`/data` 和 `/tmp` 可写。Compose 在 `cap_drop: ALL` 的基础上，只保留访问已有数据所需的 `DAC_OVERRIDE` 和 `FOWNER`，后者用于设置数据库文件权限。root 运行会放宽对挂载目录的访问，挂载范围应仅包含本项目的数据目录；只读挂载及启用 root squash 的远程文件系统仍需单独匹配存储权限。

每 30 秒检查一次 `/healthz`。Docker 的 `healthy` 只代表进程能够响应健康接口，上游采集、转发和通知仍需各自验收。日志默认保留 3 份、每份最多 10 MiB。

### NAS 目录挂载

使用 `0.1.1` 或更新版本时，可在独立的 Compose 项目目录中使用以下配置；普通 NAS 本地目录可直接创建并使用，无需额外指定 root 用户：

```yaml
services:
  ledgergate:
    container_name: ledgergate
    image: ghcr.io/sectsnow/ledgergate:latest
    restart: unless-stopped
    ports:
      - "18080:8080"
    volumes:
      - "./data:/data"
    stop_grace_period: 30s
```

`./data` 相对于 Compose 文件所在的项目目录。使用已发布的 `0.1.0` 时，在服务中补充 `user: "0:0"`。已有数据需先备份并停止旧实例，再启动新容器，不能让两个实例同时打开同一份数据库。不要删除原 `master.key` 来处理权限错误。

## 更新与备份

在系统设置中先下载完整 `.lgb` 加密备份，将备份和备份密码分开保存，再根据部署方式更新。

本地构建部署：

```bash
docker compose build --pull
docker compose up -d
docker compose ps
```

使用 GHCR 镜像部署时，先在 `.env` 中设置目标版本，再拉取并启动：

```bash
docker compose pull
docker compose up -d --no-build
docker compose ps
```

需要命令行恢复时，可使用同一镜像的 `ledgergate restore` 子命令。它只恢复到尚不存在的新目录；参数和校验方式参见[部署与数据维护中的“加密备份与恢复”](deployment.md#加密备份与恢复)。

## 从当前 systemd 数据迁移

先完成空数据容器的首次设置、采集与转发验收，再安排迁移窗口。此处步骤会停止当前服务，准备发布配置时不自动执行。

1. 从现有网页下载完整备份，保存原程序与配置；停止 `ledgergate.service`，确认其进程已退出。
2. 将宿主机整个数据目录复制到独立的迁移快照目录，包含 `master.key`、`admin-password`、`.db` 及仍存在的 WAL/SHM。复制时服务应保持停止；不直接挂载原目录，也不修改原目录权限。
3. 停止候选容器，将快照导入命名卷中一个尚不存在的新目录。例如，把下面的宿主机路径改成实际快照路径：

```bash
docker compose stop ledgergate
docker compose run --rm --no-deps \
  --user 0:0 --cap-add DAC_OVERRIDE --cap-add CHOWN \
  --entrypoint /bin/sh \
  --volume "/absolute/path/snapshot-data:/source:ro" \
  ledgergate -ec '
    test ! -e /data/imported
    mkdir /data/imported
    cp -R /source/. /data/imported/
    chown -R 10001:10001 /data/imported
  '
```

4. 在 `compose.yaml` 的 `ledgergate` 服务中添加 `environment` 配置，设置 `LEDGERGATE_DATA_DIR: /data/imported`，再执行 `docker compose up -d`。使用原管理员密码登录，核对账户、渠道、模型路由、密钥、历史记录及凭据解密。
5. 验证采集、转发、备份恢复和重建容器后数据保留。通过后再决定是否停用原 systemd 服务的开机自启及将 Docker 宿主机端口改为 8080。

需要回退时，先停止候选容器，再启动原 systemd 服务。此回退使用迁移前的原数据；容器运行后新增的配置和历史需另外备份，不能直接拼接两份 SQLite 数据。
