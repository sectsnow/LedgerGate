import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { version } from "../package.json";
import {
  Activity,
  ArrowLeft,
  ArrowUpRight,
  ArrowUpDown,
  Bell,
  BellRing,
  ChevronDown,
  ChevronRight,
  CircleHelp,
  CircleAlert,
  CircleCheck,
  CirclePause,
  CircleX,
  Clock3,
  Coins,
  Database,
  ExternalLink,
  FileText,
  FlaskConical,
  GripVertical,
  Hash,
  History,
  Inbox,
  KeyRound,
  LayoutDashboard,
  LoaderCircle,
  LogOut,
  Megaphone,
  Network,
  Pause,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Receipt,
  Search,
  SearchX,
  Server,
  Settings2,
  ShieldCheck,
  Trash2,
  Timer,
  Unplug,
  Wallet,
  Workflow,
  X,
} from "lucide-react";
import {
  api,
  channelEnabled,
  protocolNames,
  type Account,
  type AccountRow,
  type AccountSummary,
  type AnnouncementPage,
  type Channel,
  type ChannelRow,
  type ChannelBatchInput,
  type ChannelBatchResult,
  type Data,
  type Group,
  type Key,
  type RuntimeOptions,
  type PriceSyncResult,
  type RequestLog,
  type RequestDay,
  type Route,
  type Settings,
  type UpstreamRate,
} from "./api";
import "@fontsource-variable/geist";
import CopyButton from "./CopyButton";
import ImportChannels from "./ImportChannels";
import GenerateModels from "./GenerateModels";
import ModelPrice from "./ModelPrice";
import ModelTests from "./ModelTests";
import BrandMark from "./BrandMark";
import AccountLogo from "./AccountLogo";
import AccountLogoEditor from "./AccountLogoEditor";
import AccountTokenField from "./AccountTokenField";
import RequestHistory, { RequestList } from "./RequestHistory";
import ChannelBatch, { ChannelSelection } from "./ChannelBatch";
import ChannelModelSync from "./ChannelModelSync";
import ChannelModelEditor, { channelModelNames } from "./ChannelModelEditor";
import { channelModelChanges } from "./channelModels";
import useRouteOrder from "./useRouteOrder";
import { GroupRoutingSummary, RouteRoutingStatus, RoutingStrategyIcon, useGroupRouting } from "./RoutingStatus";
import { AnnouncementList, MonitorHistory, MonitoringPanels } from "./MonitorActivity";
import ThemePicker from "./ThemePicker";
import RequestQuality from "./RequestQuality";
import RequestTrend from "./RequestTrend";
import BackupPanel from "./BackupPanel";
import ProxySettings from "./ProxySettings";
import WidgetCredentialPanel from "./WidgetCredentialPanel";
import NotificationPanel from "./NotificationPanel";
import KeyModelPicker from "./KeyModelPicker";
import DataTransferPanel from "./DataTransferPanel";
import AdminPasswordPanel, { adminPasswordError } from "./AdminPasswordPanel";
import BuildInfoPanel, { buildDescription } from "./BuildInfoPanel";
import { applyTheme, readTheme, type DataTone } from "./themes";
import "./style.css";
import "./themes.css";

const pages = [
  { id: "overview", name: "运行概览", icon: LayoutDashboard },
  { id: "accounts", name: "上游账户", icon: Server },
  { id: "channels", name: "转发渠道", icon: Network },
  { id: "models", name: "模型路由", icon: Workflow },
  { id: "model-tests", name: "模型测试", icon: FlaskConical },
  { id: "keys", name: "网关密钥", icon: KeyRound },
  { id: "requests", name: "请求记录", icon: Activity },
  { id: "alerts", name: "告警中心", icon: Bell },
  { id: "rate-changes", name: "倍率变动", icon: ArrowUpDown },
  { id: "announcements", name: "上游公告", icon: Megaphone },
  { id: "settings", name: "系统设置", icon: Settings2 },
];
const strategyNames: Record<string, string> = {
  manual: "手动选择",
  weighted: "加权轮询",
  priority: "优先级故障转移",
};

function protocolsOf(channel?: Channel): string[] {
  if (channel?.protocols?.length) return channel.protocols;
  return channel?.protocol ? [channel.protocol] : [];
}

function channelLabel(channel: Channel, accounts: AccountRow[]): string {
  const account = accounts.find(
    (row) => row.account.id === channel.account_id,
  )?.account;
  return `${account?.name || "直连供应商"} / ${channel.name}`;
}
const statusNames: Record<string, string> = {
  ok: "正常",
  low: "余额偏低",
  error: "采集异常",
  pending: "等待采集",
  paused: "已暂停",
  sent: "已送达",
  local: "本地记录",
  failed: "发送失败",
};
const statusIcons: Record<string, typeof CircleCheck> = {
  ok: CircleCheck, low: Wallet, error: CircleAlert, pending: Clock3,
  paused: CirclePause, sent: CircleCheck, local: FileText, failed: CircleX,
};
const compact = (value: number) =>
  new Intl.NumberFormat("zh-CN", {
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(value);
const date = (value: string | null) =>
  value
    ? new Date(value).toLocaleString("zh-CN", { hour12: false })
    : "尚未采集";
function relativeTime(value: string) {
  const elapsed = Date.now() - new Date(value).getTime();
  if (!Number.isFinite(elapsed)) return "时间未知";
  if (elapsed < 60_000) return "刚刚";
  if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)}分钟前`;
  if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)}小时前`;
  return `${Math.floor(elapsed / 86_400_000)}天前`;
}
const money = (value: number | null) =>
  value == null
    ? "—"
    : value.toLocaleString("zh-CN", {
        minimumFractionDigits: 2,
        maximumFractionDigits: 4,
      });
const multiplier = (value: number | null) =>
  value == null
    ? "上游未提供"
    : `${value.toLocaleString("zh-CN", { maximumFractionDigits: 10 })}×`;

type MonitorAccount = (account: Account, enabled: boolean) => Promise<void>;

function accountRates(account: Account): UpstreamRate[] {
  try {
    const groups = JSON.parse(account.rates || "null")?.groups;
    if (Array.isArray(groups)) {
      return groups.sort(
        (a: UpstreamRate, b: UpstreamRate) =>
          (a.multiplier ?? Infinity) - (b.multiplier ?? Infinity) ||
          a.group.localeCompare(b.group, "zh-CN"),
      );
    }
  } catch {
    /* 尚未采集或旧快照按空列表显示。 */
  }
  return [];
}

function Badge({
  status,
  children,
}: {
  status: string;
  children?: React.ReactNode;
}) {
  const Icon = statusIcons[status] || CircleHelp;
  return (
    <span className={`badge ${status}`}>
      <Icon size={12} aria-hidden="true" />
      {children || statusNames[status] || status}
    </span>
  );
}
function Empty({
  title,
  detail,
  action,
  icon = <Inbox size={25} aria-hidden="true" />,
}: {
  title: string;
  detail?: string;
  action?: React.ReactNode;
  icon?: React.ReactNode;
}) {
  return (
    <div className="empty">
      <div className="empty-symbol">
        {icon}
      </div>
      <h3>{title}</h3>
      {detail && <p>{detail}</p>}
      {action}
    </div>
  );
}
function Button({
  busy,
  children,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { busy?: boolean }) {
  return (
    <button {...props} disabled={busy || props.disabled}>
      {busy ? <LoaderCircle size={16} className="spin" /> : null}
      {children}
    </button>
  );
}
function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: React.ReactNode;
  hint?: string;
}) {
  const id = React.useId();
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {React.Children.map(children, (child) => {
        if (
          React.isValidElement<{ id?: string; "aria-describedby"?: string }>(
            child,
          ) &&
          typeof child.type === "string" &&
          ["input", "select", "textarea"].includes(child.type)
        ) {
          return React.cloneElement(child, {
            id,
            "aria-describedby": hint ? `${id}-hint` : undefined,
          });
        }
        return child;
      })}
      {hint && <small id={`${id}-hint`}>{hint}</small>}
    </div>
  );
}
function Dialog({
  title,
  children,
  close,
  className,
}: {
  title: string;
  children: React.ReactNode;
  close: () => void;
  className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleID = React.useId();
  useEffect(() => {
    const dialog = ref.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className={className}
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
      aria-labelledby={titleID}
    >
      <header>
        <h2 id={titleID}>{title}</h2>
        <button className="icon-button" aria-label="关闭" onClick={close}>
          <X size={20} />
        </button>
      </header>
      {children}
    </dialog>
  );
}

function Login({ done, notice }: { done: () => void; notice: string }) {
  const [setupRequired, setSetupRequired] = useState<boolean | null>(null);
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let active = true;
    api<{ setup_required: boolean }>("/setup")
      .then((status) => { if (active) setSetupRequired(status.setup_required); })
      .catch((error) => { if (active) setError(error.message); });
    return () => { active = false; };
  }, []);
  return (
    <div className="login">
      <div className="login-brand">
        <BrandMark />
        <h1>LedgerGate</h1>
        <p>管理上游，连接模型。</p>
      </div>
      <form
        className="login-form"
        onSubmit={async (e) => {
          e.preventDefault();
          if (setupRequired === null) return;
          if (setupRequired) {
            const invalid = adminPasswordError(password, confirmation);
            if (invalid) { setError(invalid); return; }
          }
          setBusy(true);
          setError("");
          try {
            await api(setupRequired ? "/setup" : "/login", "POST", {
              password,
              ...(setupRequired ? { confirm_password: confirmation } : {}),
            });
            done();
          } catch (e) {
            setError((e as Error).message);
            if (setupRequired) {
              await api<{ setup_required: boolean }>("/setup")
                .then((status) => setSetupRequired(status.setup_required))
                .catch(() => undefined);
            }
          } finally {
            setBusy(false);
          }
        }}
      >
        <ShieldCheck size={25} />
        <h2>{setupRequired ? "设置管理员密码" : "登录控制台"}</h2>
        <p>
          {setupRequired === null ? "正在检查初始化状态…" : setupRequired
            ? "首次使用，请设置 8–72 字节的管理员密码，设置后自动登录。"
            : "使用管理员密码登录。"}
        </p>
        <Field label="管理员密码">
          <input
            autoFocus
            type="password"
            autoComplete={setupRequired ? "new-password" : "current-password"}
            disabled={busy || setupRequired === null}
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        {setupRequired && (
          <Field label="确认密码">
            <input
              type="password"
              autoComplete="new-password"
              required
              disabled={busy}
              value={confirmation}
              onChange={(e) => setConfirmation(e.target.value)}
            />
          </Field>
        )}
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        {notice && <p className="login-notice" role="status">{notice}</p>}
        {setupRequired === null && error && <button type="button" className="secondary" onClick={() => location.reload()}>重新连接</button>}
        <Button className="primary" busy={busy || setupRequired === null} type="submit">
          {setupRequired ? "设置并进入控制台" : "进入控制台"}
          <ArrowUpRight size={17} />
        </Button>
      </form>
    </div>
  );
}

function App() {
  const [theme, setTheme] = useState(readTheme);
  useLayoutEffect(() => applyTheme(theme), [theme]);
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [loginNotice, setLoginNotice] = useState("");
  const [page, setPage] = useState(location.hash.slice(1) || "overview");
  const [data, setData] = useState<Data | null>(null);
  const [message, setMessage] = useState("");
  const [loading, setLoading] = useState(false);
  const [accountEdit, setAccountEdit] = useState<AccountRow | "new" | null>(
    null,
  );
  const [channelEdit, setChannelEdit] = useState<ChannelRow | "new" | null>(
    null,
  );
  const [newChannelAccount, setNewChannelAccount] = useState<Account | null>(
    null,
  );
  const [showChannelModels, setShowChannelModels] = useState(false);
  const [groupEdit, setGroupEdit] = useState<Group | "new" | null>(null);
  const [priceSyncBusy, setPriceSyncBusy] = useState(false);
  const [priceSyncResult, setPriceSyncResult] = useState<PriceSyncResult | null>(null);
  const [settingsRevision, setSettingsRevision] = useState(0);
  const [generateModels, setGenerateModels] = useState(false);
  const [detail, setDetail] = useState<Account | null>(null);
  const [importAccount, setImportAccount] = useState<Account | null>(null);
  const refreshOrder = useRef({ issued: 0, applied: 0 });
  const navigate = (id: string) => {
    location.hash = id;
    setPage(id);
  };
  const refresh = useCallback(async () => {
    const order = ++refreshOrder.current.issued;
    const [
      accounts,
      channels,
      groups,
      keys,
      requests,
      alerts,
      rateChanges,
      announcements,
      settings,
      overview,
      build,
    ] = await Promise.all([
      api<Data["accounts"]>("/accounts"),
      api<Data["channels"]>("/channels"),
      api<Data["groups"]>("/groups"),
      api<Data["keys"]>("/keys"),
      api<Data["requests"]>("/requests"),
      api<Data["alerts"]>("/alerts?page=1&page_size=10"),
      api<Data["rateChanges"]>("/alerts?kind=rates_changed&page=1&page_size=10"),
      api<Data["announcements"]>("/announcements?page=1&page_size=10"),
      api<Data["settings"]>("/settings"),
      api<Data["overview"]>("/overview"),
      api<Data["build"]>("/version").catch(() => null),
    ]);
    if (order < refreshOrder.current.applied) return;
    refreshOrder.current.applied = order;
    setData({
      accounts,
      channels,
      groups,
      keys,
      requests,
      alerts,
      rateChanges,
      announcements,
      settings,
      overview,
      build,
    });
  }, []);
  useEffect(() => {
    api("/session")
      .then(() => setAuthenticated(true))
      .catch(() => setAuthenticated(false));
    const expired = (event: Event) => {
      if (event instanceof CustomEvent && typeof event.detail === "string") setLoginNotice(event.detail);
      setAuthenticated(false);
      setData(null);
      setAccountEdit(null);
      setChannelEdit(null);
      setGroupEdit(null);
      setPriceSyncResult(null);
      setGenerateModels(false);
      setDetail(null);
      setImportAccount(null);
    };
    const hash = () => setPage(location.hash.slice(1) || "overview");
    window.addEventListener("session-expired", expired);
    window.addEventListener("hashchange", hash);
    return () => {
      window.removeEventListener("session-expired", expired);
      window.removeEventListener("hashchange", hash);
    };
  }, []);
  useEffect(() => {
    if (!authenticated) return;
    refresh().catch((e) => setMessage(e.message));
    const timer = setInterval(() => {
      if (!document.hidden) refresh().catch((e) => setMessage(e.message));
    }, 15000);
    return () => {
      clearInterval(timer);
      refreshOrder.current.applied = ++refreshOrder.current.issued;
    };
  }, [authenticated, refresh]);
  async function run(task: () => Promise<unknown>, success = "已完成") {
    try {
      await task();
      await refresh();
      setMessage(success);
    } catch (e) {
      setMessage((e as Error).message);
    }
  }
  async function setMonitoring(account: Account, enabled: boolean) {
    await run(
      () => api(`/accounts/${account.id}/monitoring`, "PATCH", { enabled }),
      enabled ? "已恢复监控及关联渠道、模型路由" : "已停止监控，关联渠道和模型路由已暂停",
    );
  }
  async function removeChannel(channel: Channel) {
    try {
      await api(`/channels/${channel.id}?detach_routes=true`, "DELETE");
      await refresh();
      setMessage(`渠道“${channel.name}”已删除`);
    } catch (error) {
      setMessage((error as Error).message);
      throw error;
    }
  }
  async function batchChannels(input: ChannelBatchInput) {
    const result = await api<ChannelBatchResult>(
      "/channels/batch",
      "POST",
      input,
    );
    if (input.action === "delete")
      setMessage(
        `批量删除完成，成功 ${result.succeeded} 条，失败 ${result.failed} 条。`,
      );
    try {
      await refresh();
    } catch (error) {
      setMessage(
        `批量操作已完成，刷新列表失败，请刷新重试（${(error as Error).message}）`,
      );
    }
    return result;
  }
  if (authenticated === null)
    return (
      <div className="loading-screen">
        <LoaderCircle className="spin" />
        正在连接 LedgerGate…
      </div>
    );
  if (!authenticated) return <Login notice={loginNotice} done={() => { setLoginNotice(""); setAuthenticated(true); }} />;
  const current = pages.find((p) => p.id === page.split("/")[0]) || pages[0];
  const accountID = page.match(/^accounts\/(\d+)\/channels$/)?.[1];
  const selectedAccount = data?.accounts.find(
    (row) => row.account.id === Number(accountID),
  )?.account;
  const badAccounts =
    data?.accounts.filter(
      (r) => r.account.enabled && ["low", "error"].includes(r.account.status),
    ).length || 0;
  const addAccount = () => {
    navigate("accounts");
    setAccountEdit("new");
  };
  const addChannel = (account: Account | null = null) => {
    setNewChannelAccount(account);
    setShowChannelModels(false);
    setChannelEdit("new");
  };
  const editChannel = (row: ChannelRow, showModels = false) => {
    setShowChannelModels(showModels);
    setChannelEdit(row);
  };
  return (
    <div className="shell">
      <aside className="sidebar">
        <a className="brand" href="#overview" aria-label="LedgerGate 首页">
          <BrandMark />
          <span>
            LedgerGate<small>上游与网关</small>
          </span>
        </a>
        <div className="sidebar-label">工作空间</div>
        <nav>
          {pages.map((p, index) => (
            <React.Fragment key={p.id}>
              {index === 5 && <div className="nav-divider" />}
              <a
                href={`#${p.id}`}
                aria-label={p.name}
                aria-current={current.id === p.id ? "page" : undefined}
                className={current.id === p.id ? "active" : ""}
              >
                <p.icon size={19} aria-hidden="true" />
                <span>{p.name}</span>
                {p.id === "accounts" && badAccounts > 0 && (
                  <em>{badAccounts}</em>
                )}
              </a>
            </React.Fragment>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="connection">
            <i />
            本地控制台<span title={`前端 v${version}\n${buildDescription(data?.build)}`}>{data?.build ? `后端 v${data.build.version}${data.build.modified ? "*" : ""}` : "后端未确认"}</span>
          </div>
          <button
            onClick={() =>
              run(async () => {
                await api("/logout", "POST");
                setAuthenticated(false);
                setData(null);
              })
            }
          >
            <LogOut size={16} />
            退出登录
          </button>
        </div>
      </aside>
      <main>
        <div className="topbar">
          <span>
            工作空间
            <ChevronRight size={14} />
            <strong>{current.name}</strong>
          </span>
          <div className="topbar-actions">
          <button
            className="mobile-logout icon-button"
            aria-label="退出登录"
            onClick={() =>
              run(async () => {
                await api("/logout", "POST");
                setAuthenticated(false);
                setData(null);
              })
            }
          >
            <LogOut size={17} />
          </button>
          </div>
        </div>
        <div className="page-content">
          {accountID && (
            <button
              className="text-button account-back"
              onClick={() => navigate("accounts")}
            >
              <ArrowLeft size={15} />
              返回上游账户
            </button>
          )}
          <div className="page-header">
            <div>
              <h1>
                {accountID
                  ? `${selectedAccount?.name || "账户"}的渠道`
                  : current.name}
              </h1>
              {selectedAccount && (
                <p className="account-channel-site">
                  {selectedAccount.base_url}
                </p>
              )}
            </div>
            <div className="header-actions">
              <Button
                className="secondary"
                busy={loading}
                onClick={async () => {
                  setLoading(true);
                  try {
                    await refresh();
                  } catch (e) {
                    setMessage((e as Error).message);
                  } finally {
                    setLoading(false);
                  }
                }}
              >
                <RefreshCw size={16} />
                刷新
              </Button>
              {["overview", "accounts"].includes(current.id) && !accountID && (
                <button className="primary" onClick={addAccount}>
                  <Plus size={17} />
                  接入账户
                </button>
              )}
              {selectedAccount && (
                <button
                  className="secondary"
                  onClick={() => setImportAccount(selectedAccount)}
                >
                  导入已有密钥
                </button>
              )}
              {(current.id === "channels" || selectedAccount) && (
                <button
                  className="primary"
                  onClick={() => addChannel(selectedAccount || null)}
                >
                  <Plus size={17} />
                  添加渠道
                </button>
              )}
              {current.id === "models" && (
                <Button
                  className="secondary"
                  busy={priceSyncBusy}
                  disabled={!data?.groups.length}
                  onClick={async () => {
                    setPriceSyncBusy(true);
                    try {
                      const result = await api<PriceSyncResult>("/groups/sync-prices", "POST");
                      await refresh();
                      setPriceSyncResult(result);
                    } catch (error) {
                      setMessage((error as Error).message);
                    } finally {
                      setPriceSyncBusy(false);
                    }
                  }}
                >
                  同步价格
                </Button>
              )}
              {current.id === "models" && (
                <button
                  className="secondary"
                  onClick={() => setGenerateModels(true)}
                >
                  <Network size={17} />
                  从渠道生成
                </button>
              )}
              {current.id === "models" && (
                <button className="primary" onClick={() => setGroupEdit("new")}>
                  <Plus size={17} />
                  添加模型
                </button>
              )}
            </div>
          </div>
          {message && (
            <div className="notice" role="status">
              <span>{message}</span>
              <button
                className="icon-button"
                aria-label="关闭提示"
                onClick={() => setMessage("")}
              >
                <X size={16} />
              </button>
            </div>
          )}
          {!data ? (
            <div className="panel">
              <Empty title="正在读取工作空间" icon={<LoaderCircle size={25} className="spin" aria-hidden="true" />} />
            </div>
          ) : (
            <>
              {current.id === "overview" && (
                <Overview
                  data={data}
                  monitor={setMonitoring}
                  navigate={navigate}
                  addAccount={addAccount}
                  addChannel={() => {
                    navigate("channels");
                    addChannel();
                  }}
                />
              )}
              {current.id === "accounts" && !accountID && (
                <Accounts
                  rows={data.accounts}
                  monitor={setMonitoring}
                  channels={data.channels}
                  openChannels={(account) =>
                    navigate(`accounts/${account.id}/channels`)
                  }
                  edit={setAccountEdit}
                  detail={setDetail}
                  importKeys={setImportAccount}
                  run={run}
                />
              )}
              {current.id === "accounts" &&
                accountID &&
                (selectedAccount ? (
                  <Channels
                    key={selectedAccount.id}
                    data={data}
                    account={selectedAccount}
                    edit={editChannel}
                    create={addChannel}
                    remove={removeChannel}
                    batch={batchChannels}
                    openAccount={(account) =>
                      navigate(`accounts/${account.id}/channels`)
                    }
                    run={run}
                  />
                ) : (
                  <div className="panel">
                    <Empty
                      title="账户不存在"
                      icon={<SearchX size={25} aria-hidden="true" />}
                      detail="请返回账户列表选择可用账户。"
                    />
                  </div>
                ))}
              {current.id === "channels" && (
                <Channels
                  data={data}
                  edit={editChannel}
                  create={addChannel}
                  remove={removeChannel}
                  batch={batchChannels}
                  openAccount={(account) =>
                    navigate(`accounts/${account.id}/channels`)
                  }
                  run={run}
                />
              )}
              {current.id === "models" && (
                <Models data={data} edit={setGroupEdit} run={run} refresh={refresh} />
              )}
              {current.id === "model-tests" && (
                <ModelTests channels={data.channels} accounts={data.accounts} groups={data.groups} changed={refresh} />
              )}
              {current.id === "keys" && <Keys data={data} run={run} />}
              {current.id === "requests" && (
                <RequestHistory channels={data.channels} dialog={Dialog} />
              )}
              {["alerts", "rate-changes", "announcements"].includes(current.id) && (
                <MonitorHistory key={current.id} mode={current.id as "alerts" | "rate-changes" | "announcements"} accounts={data.accounts} revision={data} dialog={Dialog} />
              )}
              {current.id === "settings" && (
                <><BuildInfoPanel build={data.build} frontend={version} /><ThemePicker value={theme} change={setTheme} /><AdminPasswordPanel /><WidgetCredentialPanel key={`widget-${settingsRevision}`} /><ProxySettings key={settingsRevision} /><NotificationPanel key={`notifications-${settingsRevision}`} accounts={data.accounts} defaultRetries={data.settings.runtime.notification_retries} dialog={Dialog} field={Field} /><SettingsForm key={settingsRevision} settings={data.settings} run={run} /><DataTransferPanel changed={async settingsImported => { await refresh(); if (settingsImported) setSettingsRevision(value => value + 1); }} /><BackupPanel dialog={Dialog} /></>
              )}
            </>
          )}
          <footer className="page-footer">
            <span>
              <Database size={13} />
              SQLite · 每 15 秒刷新
            </span>
            <span>LedgerGate</span>
          </footer>
        </div>
      </main>
      {accountEdit && (
        <AccountForm
          row={accountEdit === "new" ? undefined : accountEdit}
          close={() => setAccountEdit(null)}
          saved={refresh}
        />
      )}
      {channelEdit && data && (
        <ChannelForm
          row={channelEdit === "new" ? undefined : channelEdit}
          showModels={showChannelModels}
          accounts={data.accounts}
          initialAccount={
            channelEdit === "new" ? newChannelAccount || undefined : undefined
          }
          close={() => setChannelEdit(null)}
          saved={refresh}
        />
      )}
      {groupEdit && data && (
        <GroupForm
          group={groupEdit === "new" ? undefined : groupEdit}
          channels={data.channels}
          accounts={data.accounts}
          close={() => setGroupEdit(null)}
          saved={refresh}
        />
      )}
      {generateModels && data && (
        <Dialog
          title="从渠道生成模型路由"
          close={() => setGenerateModels(false)}
        >
          <GenerateModels
            channels={data.channels}
            accounts={data.accounts}
            close={() => setGenerateModels(false)}
            generated={async (result) => {
              await refresh();
              setMessage(
                `已新增 ${result.created_models} 个模型、${result.added_routes} 条路由，${result.existing_routes} 条已存在${result.failures.length ? `，${result.failures.length} 条渠道需要检查` : ""}`,
              );
            }}
          />
        </Dialog>
      )}
      {priceSyncResult && (
        <Dialog title="模型价格同步结果" close={() => setPriceSyncResult(null)}>
          <p className="price-sync-summary">
            已同步 {priceSyncResult.synced} 个模型 · 手动计价 {priceSyncResult.manual} 个 · 未匹配 {priceSyncResult.unmatched.length} 个
          </p>
          <p className="field-hint">
            同步时间 {date(priceSyncResult.synced_at)}。来源：{priceSyncResult.source}。优先使用渠道所属站点报价，公共价格会标为参考价。
            <span>支持上游站点、Models.dev、LiteLLM 和 OpenRouter。</span>
          </p>
          {priceSyncResult.unmatched.length > 0 && (
            <ul className="price-unmatched-list">
              {priceSyncResult.unmatched.map(item => <li key={item.id}><strong>{item.name}</strong><span>{item.reason}</span></li>)}
            </ul>
          )}
          {!!priceSyncResult.warnings?.length && (
            <details className="price-source-warnings"><summary>部分价格来源暂不可用（{priceSyncResult.warnings.length}）</summary>
              {priceSyncResult.warnings.map((warning) => <p key={warning}>{warning}</p>)}
            </details>
          )}
          <div className="form-footer"><button className="primary" onClick={() => setPriceSyncResult(null)}>完成</button></div>
        </Dialog>
      )}
      {importAccount && (
        <Dialog
          title={`${importAccount.name} · 导入转发渠道`}
          close={() => setImportAccount(null)}
        >
          <ImportChannels
            account={importAccount}
            close={() => setImportAccount(null)}
            imported={async (result) => {
              await refresh();
              setMessage(
                `已导入 ${result.created.length} 条渠道，${result.existing.length} 条已存在`,
              );
              navigate(`accounts/${importAccount.id}/channels`);
              setImportAccount(null);
            }}
          />
        </Dialog>
      )}
      {detail && (
        <AccountDetail account={detail} close={() => setDetail(null)} />
      )}
    </div>
  );
}

function financialAmount(value: number | null, currency: string): string {
  if (value === null) return "—";
  const amount = value.toLocaleString("zh-CN", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 4,
  });
  return currency === "USD"
    ? `$${amount}`
    : `${amount} ${currency || "未设置单位"}`;
}

function chartDomain(values: (number | null)[]): [number, number] {
  const known = values.filter((value): value is number => value !== null);
  const minimum = Math.min(0, ...known);
  const maximum = Math.max(0, ...known);
  const rough = Math.max(maximum - minimum, 0.01) / 4;
  const magnitude = 10 ** Math.floor(Math.log10(rough));
  const normalized = rough / magnitude;
  let step = magnitude;
  if (normalized > 5) step *= 10;
  else if (normalized > 2) step *= 5;
  else if (normalized > 1) step *= 2;
  const lower = Math.floor(minimum / step) * step;
  const upper = Math.ceil(maximum / step) * step;
  return [lower, upper === lower ? lower + step * 4 : upper];
}

function BalanceOverview({
  summary,
  accounts,
}: {
  summary: AccountSummary;
  accounts: AccountRow[];
}) {
  const container = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(900);
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const [hoverY, setHoverY] = useState<number | null>(null);
  const tooltip = useRef<HTMLDivElement>(null);
  const tooltipID = React.useId();
  const [tooltipSize, setTooltipSize] = useState({ width: 220, height: 120 });
  const [days, setDays] = useState<7 | 14 | 30>(7);
  useEffect(() => {
    if (!container.current) return;
    const observer = new ResizeObserver((entries) =>
      setWidth(Math.max(200, entries[0].contentRect.width)),
    );
    observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  const points = summary.history.slice(-days);
  const balances = points.map((point) => point.balance);
  const consumption = points.map((point) => point.consumption);
  const partialDays = points.filter((point) =>
    (point.balance !== null && point.balance_accounts < summary.accounts) ||
    (point.consumption !== null && point.consumption_accounts < summary.accounts),
  ).length;
  const [balanceMin, balanceMax] = chartDomain(balances);
  const [costMin, costMax] = chartDomain(consumption);
  const left = 56;
  const right = 56;
  const top = 26;
  const bottom = 222;
  const plotWidth = width - left - right;
  const x = (index: number) =>
    left + (index * plotWidth) / Math.max(1, points.length - 1);
  const halfStep = plotWidth / Math.max(1, points.length - 1) / 2;
  const labelCount = Math.min(
    points.length,
    Math.max(2, Math.floor(plotWidth / 55) + 1),
  );
  const labelIndexes = new Set(
    Array.from({ length: labelCount }, (_, index) =>
      Math.round((index * (points.length - 1)) / Math.max(1, labelCount - 1)),
    ),
  );
  const balanceY = (value: number) =>
    bottom -
    ((value - balanceMin) / (balanceMax - balanceMin)) * (bottom - top);
  const costY = (value: number) =>
    bottom - ((value - costMin) / (costMax - costMin)) * (bottom - top);
  const linePath = (
    values: (number | null)[],
    y: (value: number) => number,
  ) => {
    let connected = false;
    return values.map((value, index) => {
      if (value === null) { connected = false; return ""; }
      const command = connected ? "L" : "M";
      connected = true;
      return `${command}${x(index).toFixed(2)},${y(value).toFixed(2)}`;
    }).join(" ");
  };
  const active = activeIndex ?? points.length - 1;
  const point = points[active];
  const recordedDays = points.filter(
    (_, index) => balances[index] !== null || consumption[index] !== null,
  ).length;
  const currency = summary.currency;
  useLayoutEffect(() => {
    if (!tooltip.current) return;
    const { offsetWidth, offsetHeight } = tooltip.current;
    setTooltipSize((previous) => previous.width === offsetWidth && previous.height === offsetHeight
      ? previous
      : { width: offsetWidth, height: offsetHeight });
  }, [activeIndex, point, width, currency]);
  const anchorX = x(active);
  const anchorY = hoverY ?? (top + bottom) / 2;
  const tooltipLeft = Math.max(8, Math.min(
    anchorX + 12 + tooltipSize.width > width - 8 ? anchorX - tooltipSize.width - 12 : anchorX + 12,
    width - tooltipSize.width - 8,
  ));
  const tooltipTop = Math.max(8, Math.min(
    anchorY + 12 + tooltipSize.height > 252 ? anchorY - tooltipSize.height - 12 : anchorY + 12,
    252 - tooltipSize.height,
  ));
  const inspect = (index: number, clientY: number) => {
    setActiveIndex(index);
    setHoverY(clientY - (container.current?.getBoundingClientRect().top || 0));
  };
  const tick = (value: number) =>
    value.toLocaleString("zh-CN", {
      notation: "compact",
      maximumFractionDigits: 4,
    });
  return (
    <section className="panel balance-overview" data-tone="balance">
      <div className="panel-heading">
        <div><h2 className="icon-heading"><Wallet size={18} aria-hidden="true" />余额概览</h2>
          <p>每日按实际采集快照汇总，暂停监控仍保留历史记录。</p>
        </div>
        <div className="balance-heading-controls">
          <div
            className="balance-period"
            role="group"
            aria-label="余额概览时间范围"
          >
            {([7, 14, 30] as const).map((period) => (
              <button
                key={period}
                type="button"
                aria-pressed={days === period}
                onClick={() => {
                  setDays(period);
                  setActiveIndex(null);
                  setHoverY(null);
                }}
              >
                {period}天
              </button>
            ))}
          </div>
          <span>{currency || "未设置单位"}</span>
        </div>
      </div>
      <div className="balance-legend">
        <span>
          <i className="balance-dot" />
          余额
        </span>
        <span>
          <i className="consumption-dot" />
          消费趋势
        </span>
        {partialDays > 0 && <span><i className="partial-dot" />部分采集合计</span>}
      </div>
      <div className="balance-chart" ref={container}>
        <svg
          viewBox={`0 0 ${width} 260`}
          width="100%"
          height="260"
          role="group"
          aria-label={`最近${days}天账户余额与实际消费趋势`}
        >
          <text x={left} y="12" className="balance-axis-title">余额</text>
          <text x={width - right} y="12" textAnchor="end" className="consumption-axis-title">消费</text>
          {[0, 0.25, 0.5, 0.75, 1].map((fraction) => {
            const y = top + (bottom - top) * fraction;
            return (
              <g key={fraction}>
                <line
                  x1={left}
                  x2={width - right}
                  y1={y}
                  y2={y}
                  className="balance-grid"
                />
                <text x={left - 8} y={y + 3} textAnchor="end">
                  {tick(balanceMax - fraction * (balanceMax - balanceMin))}
                </text>
                <text x={width - right + 8} y={y + 3}>
                  {tick(costMax - fraction * (costMax - costMin))}
                </text>
              </g>
            );
          })}
          <path d={linePath(balances, balanceY)} className="balance-line" />
          <path d={linePath(consumption, costY)} className="consumption-line" />
          {points.map((day, index) => (
            <g key={day.date}>
              {balances[index] !== null && (
                <circle
                  cx={x(index)}
                  cy={balanceY(balances[index]!)}
                  r={activeIndex === index ? 5 : 4}
                  className={`balance-point${activeIndex === index ? " active" : ""}`}
                />
              )}
              {consumption[index] !== null && (
                <circle
                  cx={x(index)}
                  cy={costY(consumption[index]!)}
                  r={activeIndex === index ? 5 : 4}
                  className={`consumption-point${activeIndex === index ? " active" : ""}`}
                />
              )}
              {labelIndexes.has(index) && (
                <text x={x(index)} y="248" textAnchor="middle">
                  {day.date.slice(5).replace("-", "/")}
                </text>
              )}
              <g
                className="balance-day"
                tabIndex={0}
                role="img"
                aria-label={`${day.date}，余额${day.balance_accounts < summary.accounts && day.balance !== null ? "（部分合计）" : ""} ${balances[index] === null ? "未采集" : financialAmount(balances[index], currency)}，消费${day.consumption_accounts < summary.accounts && day.consumption !== null ? "（部分合计）" : ""} ${consumption[index] === null ? "未采集" : financialAmount(consumption[index], currency)}`}
                aria-describedby={activeIndex === index ? tooltipID : undefined}
                onPointerEnter={(event) => inspect(index, event.clientY)}
                onPointerMove={(event) => inspect(index, event.clientY)}
                onPointerLeave={(event) => {
                  if (event.pointerType !== "touch") setActiveIndex(null);
                }}
                onClick={(event) => inspect(index, event.clientY)}
                onFocus={() => { setActiveIndex(index); setHoverY(null); }}
                onBlur={() => setActiveIndex(null)}
                onKeyDown={(event) => {
                  if (event.key === "Escape") setActiveIndex(null);
                }}
              >
                {activeIndex === index && (
                  <line
                    x1={x(index)}
                    x2={x(index)}
                    y1={top}
                    y2={bottom}
                    className="balance-cursor"
                  />
                )}
                <rect
                  x={index === 0 ? left : x(index) - halfStep}
                  y={top}
                  width={
                    index === 0 || index === points.length - 1
                      ? halfStep
                      : halfStep * 2
                  }
                  height={bottom - top}
                  fill="transparent"
                  className="balance-hit"
                />
              </g>
            </g>
          ))}
        </svg>
        {activeIndex !== null && point && (
          <div
            className="balance-tooltip"
            id={tooltipID}
            role="tooltip"
            aria-live="polite"
            ref={tooltip}
            style={{ left: tooltipLeft, top: tooltipTop }}
          >
            <strong className="balance-tooltip-date">
              <time dateTime={point.date}>{Number(point.date.slice(5, 7))}月{Number(point.date.slice(8, 10))}日</time>
            </strong>
            <div className="balance-tooltip-row balance-tooltip-balance">
              <span>{point.balance !== null && point.balance_accounts < summary.accounts ? "余额（部分）：" : "余额："}</span><b>{financialAmount(balances[active], currency)}</b>
            </div>
            <div className="balance-tooltip-row balance-tooltip-consumption">
              <span>{point.consumption !== null && point.consumption_accounts < summary.accounts ? "消费（部分）：" : "消费："}</span><b>{financialAmount(consumption[active], currency)}</b>
            </div>
            {(point.balance_accounts < summary.accounts || point.consumption_accounts < summary.accounts) && (
              <small>余额已采集 {point.balance_accounts}/{summary.accounts} · 消费已采集 {point.consumption_accounts}/{summary.accounts}</small>
            )}
          </div>
        )}
      </div>
      {(recordedDays < points.length || partialDays > 0) && (
        <p className="balance-history-note">
          已记录 {recordedDays}/{points.length} 天，无采集记录的日期留空。{partialDays > 0 && "相邻有数据的日期以实线连接，部分合计可查看采集覆盖数量。"}
        </p>
      )}
      <div className="balance-account-list">
        {accounts.map(({ account }) => (
          <div className="balance-account-item" key={account.id}>
            <i
              className={`account-status ${account.enabled ? account.status : "paused"}`}
            />
            <strong>{account.name}</strong>
            <span>
              {financialAmount(account.balance, account.currency)} · 今日 {financialAmount(account.today_used, account.currency)}
            </span>
          </div>
        ))}
      </div>
    </section>
  );
}

function AccountFinance({ data }: { data: Data }) {
  const summaries = data.overview.account_summaries || [];
  const currencyId = React.useId();
  const [currency, setCurrency] = useState(summaries[0]?.currency || "");
  const summary =
    summaries.find((item) => item.currency === currency) || summaries[0];
  const unit = summary?.currency || "";
  const coverage = (known: number, complete: string) =>
    summary && known < summary.accounts
      ? `已采集 ${known}/${summary.accounts} 个账户`
      : complete;
  const minimum = summary?.minimum;
  const accounts = data.accounts.filter(
    (row) => row.account.currency.trim().toUpperCase() === unit,
  );
  return (
    <>
      {summaries.length > 1 && (
        <div className="finance-currency">
          <label htmlFor={currencyId}>统计单位</label>
          <select
            id={currencyId}
            value={unit}
            onChange={(event) => setCurrency(event.target.value)}
          >
            {summaries.map((item) => (
              <option value={item.currency} key={item.currency}>
                {item.currency || "未设置单位"}
              </option>
            ))}
          </select>
        </div>
      )}
      <div className="stats-grid finance-stats">
        <Stat
          label="总余额"
          tone="balance"
          value={financialAmount(summary?.balance ?? null, unit)}
          icon={<Wallet size={18} />}
          note={
            minimum
              ? `最低 ${minimum.name} ${financialAmount(minimum.balance, unit)}${summary && summary.balance_accounts < summary.accounts ? ` · 已采集 ${summary.balance_accounts}/${summary.accounts}` : ""}`
              : "等待账户余额采集"
          }
        />
        <Stat
          label="今日总消费"
          tone="cost"
          value={financialAmount(summary?.today_used ?? null, unit)}
          icon={<Receipt size={18} aria-hidden="true" />}
          note={coverage(summary?.today_accounts || 0, "按上游实际扣费统计")}
        />
        <Stat
          label="累计消费"
          tone="cost"
          value={financialAmount(summary?.used ?? null, unit)}
          icon={<Coins size={18} aria-hidden="true" />}
          note={coverage(summary?.used_accounts || 0, "各账户累计实际扣费")}
        />
      </div>
      {summary && <BalanceOverview summary={summary} accounts={accounts} />}
    </>
  );
}

function RequestActivity({
  days,
  retentionDays,
}: {
  days: RequestDay[];
  retentionDays: number;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const latest = days.at(-1)?.date;
  useEffect(() => {
    if (scroll.current) scroll.current.scrollLeft = scroll.current.scrollWidth;
  }, [latest]);
  if (!days.length) return null;
  const active =
    days.find((day) => day.date === selected) || days[days.length - 1];
  const maximum = Math.max(1, ...days.map((day) => day.requests));
  const total = days.reduce((sum, day) => sum + day.requests, 0);
  const activeDays = days.filter((day) => day.requests > 0).length;
  const offset = (new Date(`${days[0].date}T00:00:00Z`).getUTCDay() + 6) % 7;
  const cells: (RequestDay | null)[] = [...Array(offset).fill(null), ...days];
  while (cells.length % 7) cells.push(null);
  const describe = (day: RequestDay) => {
    if (!day.requests) return `${day.date}：无汇总记录`;
    const cost = (day.cost_unknown_requests || 0) >= day.requests ? "费用无法估算" : `费用估算 $${((day.cost_micros || 0) / 1e6).toFixed(6)}`;
    return `${day.date}：${day.requests.toLocaleString("zh-CN")} 次请求 · 输入 ${(day.input_tokens || 0).toLocaleString("zh-CN")} / 输出 ${(day.output_tokens || 0).toLocaleString("zh-CN")} Token · ${cost}${day.cost_unknown_requests ? ` · ${day.cost_unknown_requests} 次费用未知，未计入估算` : ""}`;
  };
  return (
    <section className="panel request-activity">
      <div className="panel-heading">
        <div>
          <h2 className="icon-heading"><Activity size={18} aria-hidden="true" />请求活跃度</h2>
          <p>近一年 · 每格代表一天</p>
        </div>
        <span>
          {total.toLocaleString("zh-CN")} 次请求 · {activeDays} 个活跃日
        </span>
      </div>
      <div className="activity-scroll" ref={scroll}>
        <div
          className="activity-grid"
          role="group"
          aria-label="每日网关请求活跃度"
        >
          {cells.map((day, cellIndex) => {
            if (!day)
              return (
                <span
                  key={`padding-${cellIndex}`}
                  className="activity-padding"
                  aria-hidden="true"
                />
              );
            const index = cellIndex - offset;
            const level =
              day.requests === 0 ? 0 : Math.ceil((day.requests / maximum) * 4);
            return (
              <button
                key={day.date}
                type="button"
                className="activity-day activity-level"
                data-date={day.date}
                data-level={level}
                title={describe(day)}
                aria-label={describe(day)}
                aria-pressed={active.date === day.date}
                tabIndex={active.date === day.date ? 0 : -1}
                onMouseEnter={() => setSelected(day.date)}
                onFocus={() => setSelected(day.date)}
                onClick={() => setSelected(day.date)}
                onKeyDown={(event) => {
                  const moves: Record<string, number> = {
                    ArrowLeft: -7,
                    ArrowRight: 7,
                    ArrowUp: -1,
                    ArrowDown: 1,
                  };
                  const move = moves[event.key];
                  if (move === undefined) return;
                  event.preventDefault();
                  const next =
                    days[Math.max(0, Math.min(days.length - 1, index + move))];
                  event.currentTarget.parentElement
                    ?.querySelector<HTMLButtonElement>(
                      `[data-date="${next.date}"]`,
                    )
                    ?.focus();
                }}
              />
            );
          })}
        </div>
      </div>
      <div className="activity-footer">
        <span className="activity-inspector" aria-live="polite">
          {describe(active)}
        </span>
        <div className="activity-legend" aria-label="颜色越深代表请求越多">
          少
          {[0, 1, 2, 3, 4].map((level) => (
            <i
              className="activity-level"
              data-level={level}
              key={level}
              aria-hidden="true"
            />
          ))}
          多
        </div>
      </div>
      <p className="activity-note">
        按北京时间自然日统计，每日汇总保留 {retentionDays} 天，逐条请求到期清理后仍保留汇总。
      </p>
    </section>
  );
}

function Overview({
  data,
  monitor,
  navigate,
  addAccount,
  addChannel,
}: {
  data: Data;
  monitor: MonitorAccount;
  navigate: (id: string) => void;
  addAccount: () => void;
  addChannel: () => void;
}) {
  const healthy = data.accounts.filter(
    (r) => r.account.enabled && r.account.status === "ok",
  ).length;
  const enabled = data.accounts.filter((r) => r.account.enabled).length;
  const pending = data.alerts.pending;
  const { overview } = data;
  const activity = overview.activity || [];
  const lastDate = activity.at(-1)?.date || new Date(Date.now() + 8 * 3600000).toISOString().slice(0, 10);
  const firstDate = activity[0]?.date || lastDate;
  const [period, setPeriod] = useState<"today" | 1 | 7 | 14 | 30 | { start: string; end: string }>(1);
  const [customOpen, setCustomOpen] = useState(false);
  const [startDate, setStartDate] = useState(activity.at(-7)?.date || firstDate);
  const [endDate, setEndDate] = useState(lastDate);
  const [rangeError, setRangeError] = useState("");
  const days = typeof period === "object"
    ? activity.filter(day => day.date >= period.start && day.date <= period.end)
    : activity.slice(period === "today" ? -1 : -period);
  const totals = period === 1 ? overview : days.reduce((sum, day) => ({
    requests: sum.requests + day.requests,
    errors: sum.errors + day.errors,
    input_tokens: sum.input_tokens + day.input_tokens,
    output_tokens: sum.output_tokens + day.output_tokens,
    cost_micros: sum.cost_micros + day.cost_micros,
    cost_unknown_requests: sum.cost_unknown_requests + day.cost_unknown_requests,
  }), { requests: 0, errors: 0, input_tokens: 0, output_tokens: 0, cost_micros: 0, cost_unknown_requests: 0 });
  const periodLabel = typeof period === "object" ? "所选时段" : period === "today" ? "今日" : period === 1 ? "24 小时" : `${period} 天`;
  const retentionStart = activity.at(-data.settings.request_summary_retention_days)?.date || firstDate;
  const selectedStart = typeof period === "object" ? period.start : days[0]?.date;
  // 与后端相同，使用当前小时及之前 23 个小时的汇总。
  const hours = Array.from(
    { length: 24 },
    (_, i) => Math.floor(Date.now() / 3600000) * 3600 - (23 - i) * 3600,
  ).filter(hour => period !== "today" || hour >= Date.parse(`${lastDate}T00:00:00+08:00`) / 1000);
  const points = period === 1 || period === "today" ? hours.map((hour) => ({
    ...(overview.series?.find((s) => s.hour === hour) ?? { requests: 0, errors: 0, input_tokens: 0, output_tokens: 0, cost_micros: 0, cost_unknown_requests: 0 }),
    label: new Date(hour * 1000).toLocaleTimeString("zh-CN", { timeZone: "Asia/Shanghai", hour: "2-digit", minute: "2-digit", hour12: false }),
    title: new Date(hour * 1000).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai", hour12: false }),
  })) : days.map((day) => ({ ...day, label: day.date.slice(5).replace("-", "/"), title: day.date }));
  return (
    <>
      <AccountFinance data={data} />
      <RequestActivity
        days={overview.activity || []}
        retentionDays={data.settings.request_summary_retention_days}
      />
      <div className="section-heading request-period-heading">
        <h2 className="icon-heading"><Hash size={18} aria-hidden="true" />网关用量</h2>
        <div className="balance-period" role="group" aria-label="网关用量统计时段">
          {(["today", 1, 7, 14, 30] as const).map((value) => (
            <button key={value} aria-pressed={period === value} onClick={() => { setPeriod(value); setCustomOpen(false); setRangeError(""); }}>
              {value === "today" ? "今天" : value === 1 ? "近24个小时" : `${value}天`}
            </button>
          ))}
          <button aria-pressed={typeof period === "object"} aria-expanded={customOpen} onClick={() => setCustomOpen(!customOpen)}>自定义</button>
        </div>
      </div>
      {customOpen && (
        <form className="request-date-range" onSubmit={(event) => {
          event.preventDefault();
          if (!startDate || !endDate) {
            setRangeError("请选择开始日期和结束日期。");
          } else if (startDate > endDate) {
            setRangeError("开始日期不能晚于结束日期。");
          } else if (startDate < firstDate || endDate > lastDate) {
            setRangeError(`请选择 ${firstDate} 至 ${lastDate} 范围内的日期。`);
          } else {
            setPeriod({ start: startDate, end: endDate });
            setRangeError("");
          }
        }}>
          <Field label="开始日期">
            <input type="date" required min={firstDate} max={lastDate} value={startDate} onChange={event => { setStartDate(event.target.value); setRangeError(""); }} />
          </Field>
          <Field label="结束日期">
            <input type="date" required min={firstDate} max={lastDate} value={endDate} onChange={event => { setEndDate(event.target.value); setRangeError(""); }} />
          </Field>
          <button className="secondary" type="submit">应用</button>
          {rangeError && <p className="form-error" role="alert">{rangeError}</p>}
        </form>
      )}
      <div className="stats-grid">
        <Stat
          label="账户健康"
          tone="balance"
          value={
            <>
              {healthy}
              <span className="stat-denominator"> / {enabled}</span>
            </>
          }
          icon={<Server size={18} />}
          note={
            enabled === 0
              ? "接入账户后开始采集"
              : `${enabled - healthy} 个账户需要关注`
          }
        />
        <Stat
          label={`${periodLabel}请求`}
          tone="traffic"
          value={compact(totals.requests)}
          icon={<Activity size={18} />}
          note={`${totals.errors} 次失败`}
        />
        <Stat
          label={`${periodLabel} Token`}
          tone="usage"
          value={compact(totals.input_tokens + totals.output_tokens)}
          icon={<Hash size={18} aria-hidden="true" />}
          note={`输入 ${compact(totals.input_tokens)} · 输出 ${compact(totals.output_tokens)}`}
        />
        <Stat
          label={`${periodLabel}费用估算`}
          tone="cost"
          value={`$${money(totals.cost_micros / 1e6)}`}
          icon={<Coins size={18} aria-hidden="true" />}
          note={
            [totals.cost_unknown_requests
              ? `${totals.cost_unknown_requests} 次费用未知，未计入估算`
              : "按模型配置单价估算",
            pending ? `${pending} 条告警等待送达或发送失败` : ""].filter(Boolean).join(" · ")
          }
        />
      </div>
      <div className="overview-grid">
        <section className="panel traffic" data-tone="traffic">
          <div className="panel-heading">
            <div>
              <h2 className="icon-heading"><Activity size={18} aria-hidden="true" />请求趋势</h2>
              <p>{typeof period === "object"
                ? `${period.start} 至 ${period.end} · 按北京时间自然日（含首尾日期）`
                : period === "today" ? "今天 · 北京时间零点至当前 · 按小时汇总"
                  : period === 1 ? "近 24 个小时 · 包含当前小时" : `近 ${period} 天 · 按北京时间自然日（含今天）`}</p>
              {period !== 1 && selectedStart && selectedStart < retentionStart && (
                <p>每日汇总保留 {data.settings.request_summary_retention_days} 天，已清理的历史不计入统计。</p>
              )}
            </div>
          </div>
          <RequestTrend points={points} />
        </section>
        <section className="panel gateway-summary" data-tone="cost">
          <div className="panel-heading">
            <h2>网关状态</h2>
            <Network size={18} />
          </div>
          <div className="gateway-number">
            {
              data.channels.filter(
                (r) =>
                  channelEnabled(r) &&
                  new Date(r.cooldown_until).getTime() < Date.now(),
              ).length
            }
            <span>条可用渠道</span>
          </div>
          <div className="summary-row">
            <span>统一模型</span>
            <strong>{data.groups.length}</strong>
          </div>
          <div className="summary-row">
            <span>网关密钥</span>
            <strong>{data.keys.length}</strong>
          </div>
          <div className="endpoint">
            <span>API BASE URL</span>
            <code>{location.origin}/v1</code>
            <CopyButton text={`${location.origin}/v1`} />
          </div>
          <button className="text-button" onClick={() => navigate("models")}>
            配置模型路由
            <ChevronRight size={15} />
          </button>
        </section>
      </div>
      <RequestQuality range={typeof period === "object" ? `start=${period.start}&end=${period.end}` : period === 1 ? "" : `start=${days[0]?.date || lastDate}&end=${lastDate}`} refreshed={overview} />
      <div className="section-heading">
        <h2 className="icon-heading"><Server size={18} aria-hidden="true" />账户概况</h2>
        <button className="text-button" onClick={() => navigate("accounts")}>
          查看全部
          <ChevronRight size={15} />
        </button>
      </div>
      {data.accounts.length === 0 ? (
        <div className="panel">
          <Empty
            title="连接你的第一个上游"
            icon={<Unplug size={25} aria-hidden="true" />}
            detail="接入 NewAPI 或 Sub2API，集中查看账户余额、订阅与告警。"
            action={
              <div className="inline-actions">
                <button className="primary" onClick={addAccount}>
                  <Plus size={16} />
                  接入账户
                </button>
                <button className="secondary" onClick={addChannel}>
                  添加直连渠道
                </button>
              </div>
            }
          />
        </div>
      ) : (
        <AccountSections rows={data.accounts} limit={3}>
          {(row) => (
            <AccountCard
              key={row.account.id}
              row={row}
              monitor={monitor}
              channelCount={
                data.channels.filter(
                  (channel) => channel.channel.account_id === row.account.id,
                ).length
              }
              openChannels={() =>
                navigate(`accounts/${row.account.id}/channels`)
              }
              onClick={() => navigate(`accounts/${row.account.id}/channels`)}
            />
          )}
        </AccountSections>
      )}
      <MonitoringPanels data={data} navigate={navigate} dialog={Dialog} />
      <div className="recent-requests-panel">
        <section className="panel">
          <div className="panel-heading">
            <h2 className="icon-heading"><FileText size={18} aria-hidden="true" />最近请求</h2>
            <button
              className="text-button"
              onClick={() => navigate("requests")}
            >
              全部
              <ChevronRight size={14} />
            </button>
          </div>
          <Requests
            rows={data.requests.slice(0, 5)}
            channels={data.channels}
            small
          />
        </section>
      </div>
    </>
  );
}
function Stat({
  label,
  value,
  note,
  icon,
  tone = "traffic",
}: {
  label: string;
  value: React.ReactNode;
  note: string;
  icon: React.ReactNode;
  tone?: DataTone;
}) {
  return (
    <div className="stat-card" data-tone={tone}>
      <div className="stat-label">
        {label}
        {icon}
      </div>
      <div className="stat-value">{value}</div>
      <div className="stat-note">{note}</div>
    </div>
  );
}
function AccountGroups({ account }: { account: Account }) {
  const [expanded, setExpanded] = useState(false);
  const rates = accountRates(account);
  const visible = expanded ? rates : rates.slice(0, 4);
  return (
    <section
      className="account-groups"
      aria-label={`${account.name}的上游分组`}
    >
      <div className="account-groups-heading">
        <span>{rates.length} 个分组</span>
        {rates.length > 4 && (
          <button
            className="text-button account-groups-toggle"
            aria-label={`${expanded ? "收起" : "展开"} ${account.name} 的全部分组`}
            aria-expanded={expanded}
            aria-controls={`account-${account.id}-groups`}
            onClick={() => setExpanded(!expanded)}
          >
            {expanded ? "收起" : "展开全部"}
            <ChevronDown size={14} />
          </button>
        )}
      </div>
      <div className="account-groups-list" id={`account-${account.id}-groups`}>
        {visible.map((rate) => (
          <span
            className={`upstream-group${rate.multiplier == null ? " unknown" : ""}`}
            key={rate.group_id}
            title={`基础倍率：${multiplier(rate.base_multiplier)}；实际倍率：${multiplier(rate.multiplier)}`}
          >
            <span>{rate.group}</span>
            <strong>{multiplier(rate.multiplier)}</strong>
          </span>
        ))}
        {rates.length === 0 && <p>暂无分组信息，请先采集账户。</p>}
      </div>
    </section>
  );
}

function AccountSections({ rows, limit, children }: {
  rows: AccountRow[];
  limit?: number;
  children: (row: AccountRow) => React.ReactNode;
}) {
  return <div className="account-sections">{[true, false].map(enabled => {
    const accounts = rows.filter(row => row.account.enabled === enabled);
    const title = enabled ? "正在监控" : "已停止监控";
    return <section key={String(enabled)} className={`account-section ${enabled ? "monitoring" : "stopped"}`} aria-label={title}>
      <div className="account-section-heading"><h3>{enabled ? <Activity size={17} /> : <Pause size={17} />}{title}<span>{accounts.length}</span></h3>
        <p>{enabled ? "定时采集，关联渠道按各自配置转发。" : "关联渠道与模型路由已暂停，恢复监控后重新可用。"}</p>
      </div>
      {accounts.length ? <div className="account-grid">{accounts.slice(0, limit).map(children)}</div> : <p className="account-section-empty">{enabled ? "暂无正在监控的账户" : "暂无停止监控的账户"}</p>}
      {limit != null && accounts.length > limit && <p className="field-hint">显示前 {limit} 个账户，可在上游账户中查看全部。</p>}
    </section>;
  })}</div>;
}

function AccountCard({
  row,
  monitor,
  onClick,
  actions,
  channelCount,
  openChannels,
}: {
  row: AccountRow;
  monitor: MonitorAccount;
  onClick?: () => void;
  actions?: React.ReactNode;
  channelCount?: number;
  openChannels?: () => void;
}) {
  const account = row.account;
  const [monitorBusy, setMonitorBusy] = useState(false);
  return (
    <article className="account-card" data-monitoring={account.enabled ? "active" : "paused"}>
      <div className="account-card-top">
        <AccountLogo key={account.base_url} account={account} />
        <div className="account-card-name">
          <h3>{account.name}</h3>
          <a className="account-site" href={account.base_url} target="_blank" rel="noreferrer">
            {new URL(account.base_url).host}
          </a>
        </div>
        <div className="account-card-tools">
          <Badge status={account.enabled ? account.status : "paused"} />
          <a
            className="icon-button account-home-link"
            href={account.base_url}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={`打开 ${account.name} 上游主页（新标签页）`}
            title={`打开 ${account.name} 上游主页（新标签页）`}
          >
            <ExternalLink size={16} />
          </a>
        </div>
      </div>
      <div className="balance-label">
        账户余额 <span>{account.currency}</span>
      </div>
      <div className="balance-value">{financialAmount(account.balance, account.currency)}</div>
      <div className="account-meta">
        <span>今日消费</span>
        <strong>{financialAmount(account.today_used, account.currency)}</strong>
      </div>
      <div className="account-meta">
        <span>累计消费</span>
        <strong>{financialAmount(account.used, account.currency)}</strong>
      </div>
      <div className="account-meta account-check-time">
        <span>上次采集时间</span>
        <strong>
          {account.checked_at ? (
            <time
              dateTime={account.checked_at}
              title={date(account.checked_at)}
            >
              {relativeTime(account.checked_at)}
            </time>
          ) : (
            "尚未采集"
          )}
        </strong>
      </div>
      {openChannels && (
        <div className="account-meta account-channel-entry">
          <span>转发渠道</span>
          <button
            className="text-button"
            aria-label={`管理 ${account.name} 的渠道`}
            onClick={openChannels}
          >
            {channelCount} 条渠道
            <ChevronRight size={14} />
          </button>
        </div>
      )}
      <AccountGroups account={account} />
      {account.last_error && <p className="card-error">{account.last_error}</p>}
      {account.warnings && <p className="card-error">{account.warnings}</p>}
      <div className="account-card-bottom">
        {actions || (
          <button className="text-button" onClick={onClick}>
            管理
            <ChevronRight size={14} />
          </button>
        )}
      </div>
      <div className="account-card-monitor">
        <Button
          className="text-button"
          busy={monitorBusy}
          aria-label={`${account.enabled ? "暂停" : "恢复"} ${account.name} 监控`}
          onClick={async () => {
            setMonitorBusy(true);
            try {
              await monitor(account, !account.enabled);
            } finally {
              setMonitorBusy(false);
            }
          }}
        >
          {!monitorBusy &&
            (account.enabled ? <Pause size={14} /> : <Play size={14} />)}
          {account.enabled ? "暂停监控" : "恢复监控"}
        </Button>
        <span>{account.enabled ? "正在监控" : "监控、关联渠道与模型路由已暂停"}</span>
      </div>
    </article>
  );
}
function Accounts({
  rows,
  monitor,
  channels,
  openChannels,
  edit,
  detail,
  importKeys,
  run,
}: {
  rows: AccountRow[];
  monitor: MonitorAccount;
  channels: ChannelRow[];
  openChannels: (account: Account) => void;
  edit: (r: AccountRow) => void;
  detail: (a: Account) => void;
  importKeys: (a: Account) => void;
  run: (f: () => Promise<unknown>, s?: string) => Promise<void>;
}) {
  const [search, setSearch] = useState("");
  const [checking, setChecking] = useState<number | null>(null);
  const filtered = rows.filter((r) =>
    `${r.account.name} ${r.account.base_url}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  return (
    <>
      <div className="list-toolbar">
        <div className="search">
          <Search size={16} />
          <input
            aria-label="搜索账户"
            placeholder="搜索名称或站点地址"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <span>{rows.length} 个账户</span>
      </div>
      {filtered.length ? (
        <AccountSections rows={filtered}>
          {(row) => (
            <AccountCard
              key={row.account.id}
              row={row}
              monitor={monitor}
              channelCount={
                channels.filter(
                  (channel) => channel.channel.account_id === row.account.id,
                ).length
              }
              openChannels={() => openChannels(row.account)}
              actions={
                <div className="account-actions">
                  <div className="inline-actions">
                    <Button
                      className="secondary"
                      busy={checking === row.account.id}
                      onClick={async () => {
                        setChecking(row.account.id);
                        await run(
                          () =>
                            api(`/accounts/${row.account.id}/check`, "POST"),
                          "账户采集完成，请查看状态",
                        );
                        setChecking(null);
                      }}
                    >
                      采集
                    </Button>
                    <button
                      className="secondary"
                      aria-label={`从 ${row.account.name} 导入渠道`}
                      onClick={() => importKeys(row.account)}
                    >
                      导入渠道
                    </button>
                  </div>
                  <div className="inline-actions">
                    <button
                      className="icon-button"
                      aria-label={`查看 ${row.account.name} 详情`}
                      onClick={() => detail(row.account)}
                    >
                      <CircleHelp size={16} />
                    </button>
                    <button
                      className="icon-button"
                      aria-label={`编辑 ${row.account.name}`}
                      onClick={() => edit(row)}
                    >
                      <Pencil size={15} />
                    </button>
                    <button
                      className="icon-button danger"
                      aria-label={`删除 ${row.account.name}`}
                      onClick={() => {
                        if (
                          confirm(`删除账户“${row.account.name}”及其监控历史？`)
                        )
                          run(() =>
                            api(`/accounts/${row.account.id}`, "DELETE"),
                          );
                      }}
                    >
                      <Trash2 size={15} />
                    </button>
                  </div>
                </div>
              }
            />
          )}
        </AccountSections>
      ) : (
        <div className="panel">
          <Empty
            title={search ? "没有匹配的账户" : "尚未接入上游账户"}
            icon={search ? <SearchX size={25} aria-hidden="true" /> : <Server size={25} aria-hidden="true" />}
            detail="支持 NewAPI 和 Sub2API，可使用账户令牌或账号密码采集。"
          />
        </div>
      )}
    </>
  );
}

const balanceUnits: Record<string, string> = {
  USD: "USD · 美元",
  CNY: "CNY · 人民币",
  EUR: "EUR · 欧元",
  HKD: "HKD · 港币",
  JPY: "JPY · 日元",
  GBP: "GBP · 英镑",
  USDT: "USDT",
  积分: "积分",
};

function AccountForm({
  row,
  close,
  saved,
}: {
  row?: AccountRow;
  close: () => void;
  saved: () => Promise<void>;
}) {
  const formID = React.useId();
  const [form, setForm] = useState(() => ({
    name: "",
    kind: "newapi",
    base_url: "",
    enabled: true,
    use_proxy: false,
    auth_mode: "token",
    username: "",
    user_id: "",
    currency: "USD",
    quota_per_unit: 500000,
    low_balance: 0,
    block_empty: false,
    interval_seconds: 300,
    ...row?.account,
    token: "",
    refresh_token: "",
    password: "",
    login_extra: "",
    clear_login_extra: false,
  }));
  const [customCurrency, setCustomCurrency] = useState(() =>
    !Object.keys(balanceUnits).includes(row?.account.currency ?? "USD"),
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const tokenPairError = form.auth_mode === "token" && form.kind === "sub2api" && form.refresh_token.trim() && !form.token.trim()
    ? "请同时填写新的 Access Token，以保存这组 Refresh Token。" : "";
  const set = (key: string, value: unknown) =>
    setForm((previous) => ({ ...previous, [key]: value }));
  return (
    <Dialog title={row ? "编辑上游账户" : "接入上游账户"} close={close} className="account-dialog">
      <div className="account-dialog-scroll">
      {row && <AccountLogoEditor account={row.account} />}
      <form
        id={formID}
        onSubmit={async (e) => {
          e.preventDefault();
          if (tokenPairError) { setError(tokenPairError); return; }
          setBusy(true);
          setError("");
          try {
            await api(
              row ? `/accounts/${row.account.id}` : "/accounts",
              row ? "PUT" : "POST",
              form,
            );
            await saved();
            close();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="form-grid">
          <Field label="账户名称">
            <input
              autoFocus
              required
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
              placeholder="例如 主力上游"
            />
          </Field>
          <Field label="站点类型">
            <select
              value={form.kind}
              onChange={(e) => set("kind", e.target.value)}
            >
              <option value="newapi">NewAPI</option>
              <option value="sub2api">Sub2API</option>
            </select>
          </Field>
          <div className="full">
            <Field
              label="站点地址"
              hint="填写站点根地址，例如 https://api.example.com"
            >
              <input
                required
                type="url"
                value={form.base_url}
                onChange={(e) => set("base_url", e.target.value)}
                placeholder="https://"
              />
            </Field>
          </div>
          <Field label="认证方式">
            <select
              value={form.auth_mode}
              onChange={(e) => set("auth_mode", e.target.value)}
            >
              <option value="token">账户访问令牌</option>
              <option value="password">账号与密码</option>
            </select>
          </Field>
          {form.kind === "newapi" && (
            <Field label="NewAPI 用户 ID">
              <input
                value={form.user_id}
                onChange={(e) => set("user_id", e.target.value)}
                placeholder="站点要求时填写"
              />
            </Field>
          )}
          {form.auth_mode === "token" ? (
            <>
              <div className="full">
                <AccountTokenField
                  key={form.kind}
                  label={
                    form.kind === "sub2api" ? "Access Token" : "账户访问令牌"
                  }
                  hint={
                    row?.has_token
                      ? "已保存，留空保留。请使用后台账户令牌。"
                      : "使用站点后台账户令牌。转发 API Key 在渠道中配置。"
                  }
                  accountId={row?.account.id}
                  field="token"
                  hasSaved={row?.has_token}
                  required={!row?.has_token}
                  disabled={busy}
                  value={form.token}
                  onChange={value => set("token", value)}
                />
              </div>
              {form.kind === "sub2api" && (
                <div className="full">
                  <AccountTokenField
                    label="Refresh Token"
                    hint={
                      row?.has_refresh_token
                        ? "已保存，两个令牌均留空时保留。填写新 Refresh Token 时请同时填写对应的 Access Token。"
                        : "可选，用于 Access Token 过期后自动续期。填写时请同时填写对应的 Access Token。"
                    }
                    accountId={row?.account.id}
                    field="refresh_token"
                    hasSaved={row?.has_refresh_token}
                    disabled={busy}
                    value={form.refresh_token}
                    onChange={value => set("refresh_token", value)}
                    validationError={tokenPairError}
                  />
                  <p className="field-hint">
                    {form.token.trim() && !form.refresh_token.trim() && row?.has_refresh_token
                      ? "保存后将清除旧 Refresh Token；如需自动续期，请同时填写配套的新 Refresh Token。"
                      : "单独更换 Access Token 会清除旧 Refresh Token。"}
                  </p>
                </div>
              )}
            </>
          ) : (
            <>
              <Field
                label={form.kind === "sub2api" ? "登录邮箱" : "登录用户名"}
              >
                <input
                  required
                  autoComplete="off"
                  value={form.username}
                  onChange={(e) => set("username", e.target.value)}
                />
              </Field>
              <Field
                label="登录密码"
                hint={
                  row?.has_password
                    ? "已保存，留空保留"
                    : "需要验证码或二步验证的站点请使用令牌"
                }
              >
                <input
                  type="password"
                  autoComplete="new-password"
                  required={!row?.has_password}
                  value={form.password}
                  onChange={(e) => set("password", e.target.value)}
                />
              </Field>
              <div className="full">
                <Field label="登录附加参数（JSON）" hint={row?.has_login_extra ? "已加密保存，留空保留。填写新 JSON 可替换。" : "仅填写站点要求的附加字段；用户名、邮箱和密码仍使用上方填写的值。"}>
                  <textarea value={form.login_extra} onChange={event => set("login_extra", event.target.value)} placeholder={'{"custom_field":"value"}'} rows={3} />
                </Field>
                {row?.has_login_extra && <label className="checkbox"><input type="checkbox" checked={form.clear_login_extra} onChange={event => set("clear_login_extra", event.target.checked)} />清除已保存的附加参数</label>}
              </div>
            </>
          )}
          <Field label="余额单位" hint="按上游实际计价单位选择，不进行汇率换算。">
            <select
              value={customCurrency ? "__custom__" : form.currency}
              onChange={event => {
                const value = event.target.value;
                setCustomCurrency(value === "__custom__");
                set("currency", value === "__custom__" ? "" : value);
              }}
            >
              {Object.entries(balanceUnits).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
              <option value="__custom__">自定义单位…</option>
            </select>
          </Field>
          {customCurrency && <Field label="自定义余额单位"><input required maxLength={20} value={form.currency} onChange={event => set("currency", event.target.value)} placeholder="例如 额度" /></Field>}
          <Field label="余额阈值（低于此值发告警，0 = 不告警）">
            <input
              required
              type="number"
              min="0"
              step="any"
              value={form.low_balance}
              onChange={(e) => set("low_balance", Number(e.target.value))}
            />
          </Field>
          {form.kind === "newapi" && (
            <Field label="每单位原始配额" hint="按站点实际 quota 换算配置">
              <input
                required
                type="number"
                min="1"
                value={form.quota_per_unit}
                onChange={(e) => set("quota_per_unit", Number(e.target.value))}
              />
            </Field>
          )}
          <Field label="采集间隔（秒）">
            <input
              required
              type="number"
              min="30"
              max="86400"
              value={form.interval_seconds}
              onChange={(e) => set("interval_seconds", Number(e.target.value))}
            />
          </Field>
          <label className="checkbox full">
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(e) => set("enabled", e.target.checked)}
            />
            启用定时采集
          </label>
          <label className="checkbox full">
            <input type="checkbox" checked={form.use_proxy} onChange={e => set("use_proxy", e.target.checked)} />
            使用全局代理（登录、令牌续期与账户采集）
          </label>
          <label className="checkbox full">
            <input
              type="checkbox"
              checked={form.block_empty}
              onChange={(e) => set("block_empty", e.target.checked)}
            />
            最新余额确认耗尽时暂停关联渠道转发
          </label>
        </div>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
      </form>
      </div>
        <div className="form-footer">
          <button type="button" className="secondary" onClick={close}>
            取消
          </button>
          <Button className="primary" busy={busy} disabled={!!tokenPairError} type="submit" form={formID}>
            保存账户
          </Button>
        </div>
    </Dialog>
  );
}

function AccountDetail({
  account,
  close,
}: {
  account: Account;
  close: () => void;
}) {
  const [tab, setTab] = useState("history");
  const [history, setHistory] = useState<
    {
      id: number;
      balance: number | null;
      used: number | null;
      today_used: number | null;
      currency: string;
      status: string;
      created_at: string;
    }[]
  >([]);
  const [error, setError] = useState("");
  const [announcements, setAnnouncements] = useState<AnnouncementPage | null>(null);
  useEffect(() => {
    api<typeof history>(`/accounts/${account.id}/history`)
      .then(setHistory)
      .catch((e) => setError(e.message));
  }, [account.id]);
  useEffect(() => {
    if (tab !== "announcements") return;
    const controller = new AbortController();
    api<AnnouncementPage>(`/announcements?account_id=${account.id}&page_size=200`, "GET", undefined, controller.signal)
      .then(value => { if (!controller.signal.aborted) setAnnouncements(value); })
      .catch(error => { if (!controller.signal.aborted) setError(error.message); });
    return () => controller.abort();
  }, [account.id, account.announcements, tab]);
  const raw =
    tab === "rates"
      ? account.rates
      : tab === "subscriptions"
        ? account.subscriptions
        : account.announcements;
  let pretty = raw;
  const rates = accountRates(account);
  try {
    pretty = JSON.stringify(JSON.parse(raw || "null"), null, 2);
  } catch {
    /* 显示站点返回的原文。 */
  }
  return (
    <Dialog title={account.name} close={close}>
      {account.warnings && <p className="form-error">{account.warnings}</p>}
      <div className="tabs">
        {[
          ["history", "余额历史"],
          ["rates", "倍率"],
          ["subscriptions", "订阅"],
          ["announcements", "公告"],
        ].map(([id, label]) => (
          <button
            key={id}
            className={tab === id ? "selected" : ""}
            onClick={() => setTab(id)}
          >
            {label}
          </button>
        ))}
      </div>
      {error && <p className="form-error">{error}</p>}
      {tab === "history" ? (
        <div className="table-scroll detail-history">
          <table>
            <thead>
              <tr>
                <th>采集时间</th>
                <th>余额</th>
                <th>当日消费</th>
                <th>累计消费</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              {history.map((row) => (
                <tr key={row.id}>
                  <td>{date(row.created_at)}</td>
                  <td>
                    {financialAmount(row.balance, row.currency)}
                  </td>
                  <td>{financialAmount(row.today_used, row.currency)}</td>
                  <td>{financialAmount(row.used, row.currency)}</td>
                  <td>
                    <Badge status={row.status} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {history.length === 0 && <Empty title="暂无采集记录" icon={<History size={25} aria-hidden="true" />} />}
        </div>
      ) : tab === "rates" ? (
        <div className="table-scroll detail-history">
          {rates.length ? (
            <table>
              <thead>
                <tr>
                  <th>上游分组</th>
                  <th>基础倍率</th>
                  <th>实际倍率</th>
                </tr>
              </thead>
              <tbody>
                {rates.map((rate) => (
                  <tr key={rate.group_id}>
                    <td>{rate.group}</td>
                    <td>{multiplier(rate.base_multiplier)}</td>
                    <td>
                      <strong>{multiplier(rate.multiplier)}</strong>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <Empty
              title="暂无倍率数据"
              icon={<ArrowUpDown size={25} aria-hidden="true" />}
              detail="采集账户后可查看上游分组倍率。"
            />
          )}
        </div>
      ) : (
        tab === "announcements" ? <><AnnouncementList rows={announcements?.items || []} dialog={Dialog} />{announcements && announcements.total > announcements.items.length && <p className="field-hint">此处显示前 200 条，全部公告可在“上游公告”中按账户筛选查看。</p>}</> : <pre className="data-view">{pretty || "暂无采集数据"}</pre>
      )}
    </Dialog>
  );
}

function Channels({
  data,
  account,
  edit,
  create,
  openAccount,
  run,
  remove,
  batch,
}: {
  data: Data;
  account?: Account;
  edit: (row: ChannelRow, showModels?: boolean) => void;
  create: (account: Account | null) => void;
  openAccount: (account: Account) => void;
  run: (f: () => Promise<unknown>, s?: string) => Promise<void>;
  remove: (channel: Channel) => Promise<void>;
  batch: (input: ChannelBatchInput) => Promise<ChannelBatchResult>;
}) {
  const [search, setSearch] = useState("");
  const [deleting, setDeleting] = useState<Channel | null>(null);
  const [deleteError, setDeleteError] = useState("");
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [selected, setSelected] = useState<number[]>([]);
  const [batchBusy, setBatchBusy] = useState(false);
  const [syncing, setSyncing] = useState<Channel[] | null>(null);
  const [toggleError, setToggleError] = useState("");
  async function toggleChannel(channel: Channel) {
    setBatchBusy(true);
    setToggleError("");
    try {
      const result = await batch({
        ids: [channel.id],
        action: channel.enabled ? "disable" : "enable",
      });
      if (result.failed) {
        throw new Error(result.results[0]?.error || "更新渠道状态失败");
      }
    } catch (error) {
      setToggleError(`渠道“${channel.name}”：${(error as Error).message}`);
    } finally {
      setBatchBusy(false);
    }
  }
  async function acknowledgeModels(row: ChannelRow) {
    setBatchBusy(true);
    setToggleError("");
    try {
      await api(`/channels/${row.channel.id}/models/acknowledge`, "POST", { revision: row.models_revision });
      await run(() => Promise.resolve(), "已确认模型变化，当前选择保持不变");
    } catch (error) {
      setToggleError(`渠道“${row.channel.name}”：${(error as Error).message}`);
    } finally {
      setBatchBusy(false);
    }
  }
  const affectedGroups = deleting
    ? data.groups
        .filter((group) =>
          group.routes.some((route) => route.channel_id === deleting.id),
        )
        .map((group) => ({
          name: group.name,
          remaining: group.routes.filter(
            (route) => route.channel_id !== deleting.id,
          ).length,
        }))
    : [];
  const query = search.trim().toLowerCase();
  const scoped = account
    ? data.channels.filter((row) => row.channel.account_id === account.id)
    : data.channels;
  useEffect(() => {
    const ids = new Set(
      data.channels
        .filter((row) => !account || row.channel.account_id === account.id)
        .map((row) => row.channel.id),
    );
    setSelected((previous) => previous.filter((id) => ids.has(id)));
  }, [data.channels, account?.id]);
  const groups: { account: Account | null; rows: ChannelRow[] }[] = account
    ? [{ account, rows: scoped }]
    : [
        ...data.accounts.map((row) => ({
          account: row.account,
          rows: scoped.filter(
            (channel) => channel.channel.account_id === row.account.id,
          ),
        })),
        {
          account: null,
          rows: scoped.filter((row) => row.channel.account_id == null),
        },
      ];
  const visibleGroups = groups
    .map((group) => ({
      ...group,
      rows: group.rows.filter((row) =>
        [
          group.account?.name || "直连供应商",
          group.account?.base_url || "",
          row.channel.name,
          row.channel.base_url,
          row.channel.models,
          ...protocolsOf(row.channel).map(
            (protocol) => protocolNames[protocol],
          ),
        ]
          .join(" ")
          .toLowerCase()
          .includes(query),
      ),
    }))
    .filter((group) => group.rows.length > 0 || (account && !query));
  const visibleCount = visibleGroups.reduce(
    (total, group) => total + group.rows.length,
    0,
  );
  const visibleIDs = visibleGroups.flatMap((group) =>
    group.rows.map((row) => row.channel.id),
  );
  const selectedChannels = scoped
    .filter((row) => selected.includes(row.channel.id))
    .map((row) => row.channel);
  function toggleSelection(ids: number[], checked: boolean) {
    setSelected((previous) =>
      checked
        ? [...new Set([...previous, ...ids])]
        : previous.filter((id) => !ids.includes(id)),
    );
  }
  return (
    <>
      <div className="list-toolbar channel-toolbar">
        <div className="search">
          <Search size={16} />
          <input
            aria-label="搜索渠道"
            placeholder="搜索账户、渠道、模型或协议"
            value={search}
            disabled={batchBusy}
            onChange={(e) => {
              setSearch(e.target.value);
              setSelected([]);
            }}
          />
        </div>
        <span>
          {query ? `${visibleCount} / ${scoped.length}` : scoped.length} 条渠道
        </span>
      </div>
      {toggleError && <p className="form-error" role="alert">{toggleError}</p>}
      {(selectedChannels.length > 0 || batchBusy) && (
        <div className="channel-batch-panel">
          <div className="channel-batch-selection">
            <label className="checkbox">
              <ChannelSelection
                label="选择当前筛选的全部渠道"
                selected={
                  visibleIDs.filter((id) => selected.includes(id)).length
                }
                total={visibleCount}
                disabled={batchBusy}
                toggle={(checked) => toggleSelection(visibleIDs, checked)}
              />
              {query ? "全选筛选结果" : "全选当前列表"}
            </label>
            <span>已选 {selectedChannels.length} 条渠道</span>
            <button
              className="text-button"
              disabled={batchBusy || selected.length === 0}
              onClick={() => setSelected([])}
            >
              清空选择
            </button>
          </div>
          <ChannelBatch
            channels={selectedChannels}
            sync={() => setSyncing(selectedChannels)}
            groups={data.groups}
            busy={batchBusy}
            setBusy={setBatchBusy}
            execute={batch}
            completed={(result) =>
              setSelected(
                result.results
                  .filter((item) => !item.success)
                  .map((item) => item.id),
              )
            }
            dialog={Dialog}
          />
        </div>
      )}
      {visibleGroups.length === 0 ? (
        <div className="panel">
          <Empty
            title={query ? "没有匹配的渠道" : "尚未添加转发渠道"}
            icon={query ? <SearchX size={25} aria-hidden="true" /> : <Unplug size={25} aria-hidden="true" />}
            detail={
              query
                ? "试试其他名称、模型或协议。"
                : "从上游账户进入渠道管理，或添加直连供应商。"
            }
          />
        </div>
      ) : (
        <div className="channel-groups">
          {visibleGroups.map((group) => (
            <details
              className="panel channel-group"
              key={`${group.account?.id || "direct"}:${query}`}
              open={!!account || !!query}
              aria-label={`${group.account?.name || "直连供应商"}的渠道`}
            >
              <summary
                className="panel-heading channel-group-heading"
                aria-label={`${group.account?.name || "直连供应商"}的渠道，${group.rows.length}条`}
              >
                <div className="channel-group-title">
                  <ChevronRight
                    size={18}
                    className="channel-group-chevron"
                    aria-hidden="true"
                  />
                  <div>
                    <h2>
                      {group.account && !account ? (
                        <button
                          className="channel-group-name"
                          onClick={(event) => {
                            event.preventDefault();
                            openAccount(group.account!);
                          }}
                        >
                          {group.account.name}
                          <ChevronRight size={15} />
                        </button>
                      ) : (
                        group.account?.name || "直连供应商"
                      )}
                    </h2>
                    {group.account && <p>{group.account.base_url}</p>}
                  </div>
                </div>
                <div className="inline-actions">
                  <span className="channel-count">
                    {group.rows.length} 条渠道
                  </span>
                  <button
                    className="secondary"
                    disabled={batchBusy}
                    onClick={(event) => {
                      event.preventDefault();
                      create(group.account);
                    }}
                  >
                    <Plus size={15} />
                    添加渠道
                  </button>
                </div>
              </summary>
              {group.rows.length ? (
                <ChannelTable
                  rows={group.rows}
                  edit={edit}
                  sync={(channel) => setSyncing([channel])}
                  toggle={toggleChannel}
                  acknowledge={acknowledgeModels}
                  selected={selected}
                  busy={batchBusy}
                  selectionLabel={`选择${group.account?.name || "直连供应商"}的全部渠道`}
                  select={toggleSelection}
                  remove={(channel) => {
                    setDeleteError("");
                    setDeleting(channel);
                  }}
                />
              ) : (
                <Empty
                  title="该账户还没有渠道"
                  icon={<Unplug size={25} aria-hidden="true" />}
                  detail="点击添加渠道，账户归属与站点地址会自动填入。"
                />
              )}
            </details>
          ))}
        </div>
      )}
      {syncing && (
        <Dialog
          title={syncing.length === 1 ? `同步 ${syncing[0].name} 的模型` : "批量同步渠道模型"}
          close={() => { if (!batchBusy) setSyncing(null); }}
        >
          <ChannelModelSync
            channels={syncing}
            busy={batchBusy}
            setBusy={setBatchBusy}
            close={() => setSyncing(null)}
            saved={(count) => run(() => Promise.resolve(), `已同步 ${count} 条渠道的所选模型`)}
          />
        </Dialog>
      )}
      {deleting && (
        <Dialog title="删除转发渠道" close={() => setDeleting(null)}>
          <p className="delete-description">
            确认删除渠道“{deleting.name}
            ”？此操作会删除本地渠道配置及其模型路由引用。
          </p>
          {affectedGroups.length > 0 && (
            <div className="delete-references">
              <p>以下模型会移除该渠道</p>
              <ul>
                {affectedGroups.map((group) => (
                  <li key={group.name}>
                    {group.name}
                    <span>删除后剩余 {group.remaining} 条路由</span>
                  </li>
                ))}
              </ul>
              {affectedGroups.some((group) => group.remaining === 0) && (
                <p className="form-error">
                  剩余 0 条路由的模型将暂时无法转发。
                </p>
              )}
            </div>
          )}
          {deleteError && (
            <p className="form-error" role="alert">
              {deleteError}
            </p>
          )}
          <div className="form-footer">
            <button
              className="secondary"
              disabled={deleteBusy}
              onClick={() => setDeleting(null)}
            >
              取消
            </button>
            <Button
              className="destructive"
              busy={deleteBusy}
              onClick={async () => {
                setDeleteBusy(true);
                setDeleteError("");
                try {
                  await remove(deleting);
                  setDeleting(null);
                } catch (error) {
                  setDeleteError((error as Error).message);
                } finally {
                  setDeleteBusy(false);
                }
              }}
            >
              删除渠道
            </Button>
          </div>
        </Dialog>
      )}
    </>
  );
}

function ChannelRate({ row }: { row?: ChannelRow }) {
  const rate = row?.rate;
  return (
    <td className="channel-rate">
      <strong>{rate ? multiplier(rate.multiplier) : "—"}</strong>
      {row && (
        <small className="table-sub" title={rate?.group}>
          {rate?.group ||
            (row.channel.account_id ? "未匹配上游分组" : "未关联账户")}
        </small>
      )}
      {rate?.base_multiplier != null &&
        rate.multiplier != null &&
        rate.base_multiplier !== rate.multiplier && (
          <small className="table-sub">
            基础 {multiplier(rate.base_multiplier)}
          </small>
        )}
      {row?.rate_stale && (
        <small className="table-sub">上次采集，最新倍率采集失败</small>
      )}
    </td>
  );
}

function ChannelTable({
  rows,
  edit,
  sync,
  toggle,
  acknowledge,
  remove,
  selected,
  busy,
  selectionLabel,
  select,
}: {
  rows: ChannelRow[];
  edit: (row: ChannelRow, showModels?: boolean) => void;
  sync: (channel: Channel) => void;
  toggle: (channel: Channel) => Promise<void>;
  acknowledge: (row: ChannelRow) => Promise<void>;
  remove: (channel: Channel) => void;
  selected: number[];
  busy: boolean;
  selectionLabel: string;
  select: (ids: number[], checked: boolean) => void;
}) {
  return (
    <div className="table-scroll channel-table-scroll">
      <table>
        <thead>
          <tr>
            <th className="channel-selection-cell">
              <ChannelSelection
                label={selectionLabel}
                selected={
                  rows.filter((row) => selected.includes(row.channel.id)).length
                }
                total={rows.length}
                disabled={busy}
                toggle={(checked) =>
                  select(
                    rows.map((row) => row.channel.id),
                    checked,
                  )
                }
              />
            </th>
            <th>渠道</th>
            <th>上游协议</th>
            <th>倍率</th>
            <th>模型</th>
            <th>状态</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const channel = row.channel;
            const cooling = new Date(row.cooldown_until).getTime() > Date.now();
            const models = channelModelNames(channel.models);
            const changes = channelModelChanges(channel);
            const changed = changes.added.length > 0 || changes.removed.length > 0;
            return (
              <tr key={channel.id}>
                <td className="channel-selection-cell">
                  <input
                    className="key-selection"
                    type="checkbox"
                    aria-label={`选择渠道 ${channel.name}`}
                    disabled={busy}
                    checked={selected.includes(channel.id)}
                    onChange={(event) =>
                      select([channel.id], event.target.checked)
                    }
                  />
                </td>
                <td>
                  <strong>{channel.name}</strong>
                  <small className="table-sub" title={channel.base_url}>
                    {channel.base_url}
                  </small>
                </td>
                <td>
                  <span className="protocol">
                    {protocolsOf(channel)
                      .map((protocol) => protocolNames[protocol])
                      .join(" / ")}
                  </span>
                </td>
                <ChannelRate row={row} />
                <td>
                  <button
                    type="button"
                    className="text-button"
                    aria-label={`管理 ${channel.name} 的模型`}
                    disabled={busy}
                    onClick={() => edit(row, true)}
                  >
                    {models.length} 个模型
                  </button>
                  <button
                    className="icon-button"
                    aria-label={`同步 ${channel.name} 模型`}
                    disabled={busy}
                    onClick={() => sync(channel)}
                  >
                    <RefreshCw size={14} />
                  </button>
                  {channel.models_checked_at && <small className="table-sub" title={`上次成功检查：${date(channel.models_checked_at)}`}>已检查 · {relativeTime(channel.models_checked_at)}</small>}
                  {changed && <details className="discovered-models"><summary>{[changes.added.length ? `新增 ${changes.added.length} 个` : "", changes.removed.length ? `未返回 ${changes.removed.length} 个` : ""].filter(Boolean).join(" · ")}</summary>
                    {changes.added.length > 0 && <p>上游新增：{changes.added.join("、")}</p>}
                    {changes.removed.length > 0 && <p>上游未返回：{changes.removed.map(model => models.includes(model) ? `${model}（已选）` : model).join("、")}</p>}
                    <small>相对上次确认结果的变化，当前选择保持不变。</small>
                    <div className="discovered-model-actions">
                      <button className="text-button" type="button" disabled={busy} onClick={() => sync(channel)}>查看并同步</button>
                      <button className="text-button" type="button" disabled={busy} onClick={() => void acknowledge(row)}>保持当前选择</button>
                    </div>
                  </details>}
                  {!changed && changes.missing.length > 0 && <small className="table-sub" title={changes.missing.join("、")}>已保留 {changes.missing.length} 个上游未返回的模型</small>}
                  {channel.models_sync_error && <small className="table-sub price-match-error">发现失败：{channel.models_sync_error}</small>}
                </td>
                <td>
                  <div className="channel-state">
                    <button
                      type="button"
                      role="switch"
                      className="channel-switch"
                      aria-label={`${channel.name} 启用状态`}
                      aria-checked={channelEnabled(row)}
                      title={row.monitoring_paused ? "请先恢复上游账户监控" : channel.enabled ? "点击停用此渠道" : "点击启用此渠道"}
                      disabled={busy || row.monitoring_paused}
                      onClick={() => toggle(channel)}
                    >
                      <span aria-hidden="true" />
                    </button>
                  <Badge
                    status={
                      row.monitoring_paused ? "low" : !channel.enabled ? "paused" : cooling ? "low" : "ok"
                    }
                  >
                    {row.monitoring_paused ? "监控已停止"
                      : !channel.enabled
                      ? channel.auto_disabled_reason ? "自动停用" : "已停用"
                      : cooling
                        ? "冷却中"
                        : "已启用"}
                  </Badge>
                  </div>
                  {row.monitoring_paused && <small className="table-sub channel-monitoring-paused">关联路由暂停，恢复账户监控后解除。</small>}
                  {!channel.enabled && channel.auto_disabled_reason && (
                    <small className="table-sub channel-auto-disabled" title={channel.auto_disabled_reason}>
                      {channel.auto_disabled_reason}
                    </small>
                  )}
                  {cooling && (
                    <small className="table-sub">{row.last_error}</small>
                  )}
                </td>
                <td>
                  <div className="inline-actions">
                    <button
                      className="icon-button"
                      aria-label={`编辑 ${channel.name}`}
                      disabled={busy}
                      onClick={() => edit(row)}
                    >
                      <Pencil size={16} />
                    </button>
                    <button
                      className="icon-button danger"
                      aria-label={`删除 ${channel.name}`}
                      disabled={busy}
                      onClick={() => remove(channel)}
                    >
                      <Trash2 size={16} />
                    </button>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function ChannelForm({
  row,
  showModels = false,
  accounts,
  initialAccount,
  close,
  saved,
}: {
  row?: ChannelRow;
  showModels?: boolean;
  accounts: AccountRow[];
  initialAccount?: Account;
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [form, setForm] = useState(() => ({
    name: "",
    account_id: initialAccount?.id ?? null,
    base_url: initialAccount?.base_url || "",
    enabled: true,
    use_proxy: initialAccount?.use_proxy ?? false,
    timeout_seconds: 120,
    cooldown_seconds: 60,
    ...row?.channel,
    protocols: row ? protocolsOf(row.channel) : Object.keys(protocolNames),
    api_key: "",
  }));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [models, setModels] = useState(() => channelModelNames(row?.channel.models));
  const [modelsDirty, setModelsDirty] = useState(false);
  const modelsSection = useRef<HTMLDetailsElement>(null);
  useEffect(() => {
    if (showModels) modelsSection.current?.scrollIntoView({ block: "start" });
  }, [showModels]);
  const [keySource, setKeySource] = useState(
    !row && initialAccount ? "upstream" : "manual",
  );
  const [upstreamGroups, setUpstreamGroups] = useState<UpstreamRate[]>([]);
  const [groupID, setGroupID] = useState("");
  const [groupError, setGroupError] = useState("");
  const [groupLoading, setGroupLoading] = useState(false);
  const [groupReload, setGroupReload] = useState(0);
  const requestID = useRef("");
  const createUpstream =
    !row && form.account_id != null && keySource === "upstream";
  useEffect(() => {
    if (!createUpstream) return;
    let active = true;
    setUpstreamGroups([]);
    setGroupID("");
    setGroupError("");
    setGroupLoading(true);
    api<UpstreamRate[]>(`/accounts/${form.account_id}/upstream-groups`)
      .then((groups) => {
        if (active)
          setUpstreamGroups(
            groups.filter((group) => group.group_id !== "auto"),
          );
      })
      .catch((error) => {
        if (active) setGroupError((error as Error).message);
      })
      .finally(() => {
        if (active) setGroupLoading(false);
      });
    return () => {
      active = false;
    };
  }, [createUpstream, form.account_id, groupReload]);
  const set = (key: string, value: unknown) =>
    setForm((previous) => ({ ...previous, [key]: value }));
  return (
    <Dialog title={row ? "编辑转发渠道" : "添加转发渠道"} close={close}>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            if (createUpstream && !requestID.current) {
              requestID.current = Array.from(
                crypto.getRandomValues(new Uint8Array(16)),
                (byte) => byte.toString(16).padStart(2, "0"),
              ).join("");
            }
            const payload = {
              ...form,
              models: modelsDirty ? models : undefined,
              expected_models: modelsDirty ? row?.channel.models || "" : undefined,
            };
            await api(
              createUpstream
                ? `/accounts/${form.account_id}/create-channel`
                : row
                  ? `/channels/${row.channel.id}`
                  : "/channels",
              row ? "PUT" : "POST",
              createUpstream
                ? { ...payload, group_id: groupID, request_id: requestID.current }
                : payload,
            );
            await saved();
            close();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="form-grid">
          <Field label="渠道名称">
            <input
              autoFocus
              required
              disabled={busy}
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </Field>
          <fieldset className="protocol-selector" disabled={busy}>
            <legend>上游协议</legend>
            <div className="protocol-options">
              {Object.entries(protocolNames).map(([id, name]) => (
                <label className="checkbox" key={id}>
                  <input
                    type="checkbox"
                    checked={form.protocols.includes(id)}
                    onChange={(e) =>
                      setForm((previous) => ({
                        ...previous,
                        protocols: e.target.checked
                          ? [...previous.protocols, id]
                          : previous.protocols.filter((p) => p !== id),
                      }))
                    }
                  />
                  {name}
                </label>
              ))}
            </div>
            <small>可多选，转发时优先使用客户端的协议。</small>
          </fieldset>
          <div className="full">
            <Field label="关联账户">
              <select
                value={form.account_id || ""}
                disabled={busy}
                onChange={(e) => {
                  const id = Number(e.target.value) || null;
                  if (!row) setKeySource(id ? "upstream" : "manual");
                  setForm((previous) => ({
                    ...previous,
                    account_id: id,
                    use_proxy: row ? previous.use_proxy : accounts.find(a => a.account.id === id)?.account.use_proxy ?? false,
                    base_url:
                      accounts.find((a) => a.account.id === id)?.account
                        .base_url || previous.base_url,
                  }));
                }}
              >
                <option value="">直连供应商</option>
                {accounts.map((row) => (
                  <option key={row.account.id} value={row.account.id}>
                    {row.account.name}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          {!row && form.account_id != null && (
            <div className="full">
              <Field label="密钥来源">
                <select
                  value={keySource}
                  disabled={busy}
                  onChange={(event) => {
                    setKeySource(event.target.value);
                    if (event.target.value === "upstream") {
                      set(
                        "base_url",
                        accounts.find(
                          (row) => row.account.id === form.account_id,
                        )?.account.base_url || form.base_url,
                      );
                    }
                  }}
                >
                  <option value="upstream">在上游生成新密钥</option>
                  <option value="manual">手动填写已有密钥</option>
                </select>
              </Field>
            </div>
          )}
          {createUpstream && (
            <div className="full">
              <Field
                label="上游分组"
                hint="将在所选分组创建一把不限额度、不过期的密钥，并加密保存到此渠道。"
              >
                <select
                  required
                  value={groupID}
                  disabled={busy || groupLoading}
                  onChange={(event) => setGroupID(event.target.value)}
                >
                  <option value="">
                    {groupLoading ? "正在读取上游分组…" : "选择上游分组"}
                  </option>
                  {upstreamGroups.map((group) => (
                    <option key={group.group_id} value={group.group_id}>
                      {group.group} · {multiplier(group.multiplier)}
                    </option>
                  ))}
                </select>
              </Field>
              {groupError && (
                <p className="form-error" role="alert">
                  {groupError}
                </p>
              )}
              {!groupLoading && !groupError && upstreamGroups.length === 0 && (
                <p className="field-hint">上游当前没有可用分组。</p>
              )}
              <button
                type="button"
                className="text-button"
                disabled={busy || groupLoading}
                onClick={() => setGroupReload((value) => value + 1)}
              >
                <RefreshCw size={14} />
                重新加载上游分组
              </button>
            </div>
          )}
          <div className="full">
            <Field label="API 地址" hint="支持根地址或以 /v1 结尾的地址">
              <input
                required
                type="url"
                value={form.base_url}
                readOnly={createUpstream}
                onChange={(e) => set("base_url", e.target.value)}
                placeholder="https://api.example.com/v1"
              />
            </Field>
          </div>
          {!createUpstream && (
            <div className="full">
              <Field
                label="上游 API Key"
                hint={
                  row?.has_key
                    ? "已保存，留空保留。更换地址时请重新填写。"
                    : undefined
                }
              >
                <input
                  required={!row?.has_key}
                  type="password"
                  autoComplete="off"
                  value={form.api_key}
                  onChange={(e) => set("api_key", e.target.value)}
                />
              </Field>
            </div>
          )}
          <Field label="单次请求超时（秒）" hint="非流式限制完整响应时间；流式限制首个有效输出，与系统首输出超时取较小值。">
            <input
              required
              type="number"
              min="1"
              max="600"
              value={form.timeout_seconds}
              onChange={(e) => set("timeout_seconds", Number(e.target.value))}
            />
          </Field>
          <Field label="失败冷却（秒）">
            <input
              required
              type="number"
              min="1"
              max="3600"
              value={form.cooldown_seconds}
              onChange={(e) => set("cooldown_seconds", Number(e.target.value))}
            />
          </Field>
          <label className="checkbox full">
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(e) => set("enabled", e.target.checked)}
            />
            启用渠道
          </label>
          <label className="checkbox full">
            <input type="checkbox" checked={form.use_proxy} onChange={e => set("use_proxy", e.target.checked)} />
            使用全局代理（转发、模型同步与模型测试）
          </label>
          {row && (
            <details className="full channel-models-section" open={showModels} ref={modelsSection}>
              <summary>模型列表 · {models.length} 个{modelsDirty ? " · 待保存" : ""}</summary>
              <ChannelModelEditor models={models} disabled={busy} change={(value) => {
                setModels(value);
                setModelsDirty(true);
              }} />
            </details>
          )}
        </div>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-footer">
          <button
            className="secondary"
            type="button"
            disabled={busy}
            onClick={close}
          >
            取消
          </button>
          <Button
            className="primary"
            busy={busy}
            type="submit"
            disabled={
              form.protocols.length === 0 ||
              (createUpstream && (groupLoading || !!groupError || !groupID))
            }
          >
            {createUpstream ? "在上游创建并保存" : "保存渠道"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function Models({
  data,
  edit,
  run,
  refresh,
}: {
  data: Data;
  edit: (g: Group) => void;
  run: (f: () => Promise<unknown>, s?: string) => Promise<void>;
  refresh: () => Promise<void>;
}) {
  const [switching, setSwitching] = useState<number | null>(null);
  const [removing, setRemoving] = useState<number | null>(null);
  const [selected, setSelected] = useState(new Set<number>());
  const [deleting, setDeleting] = useState<{ groups: Group[]; all: boolean } | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteResult, setDeleteResult] = useState<{ removed: number; failed: { id: number; name: string; error: string }[]; refreshError: string } | null>(null);
  const order = useRouteOrder(refresh);
  const routing = useGroupRouting();
  const mutating = switching !== null || removing !== null || order.savingGroup !== null || deleteBusy;
  const actionsBusy = mutating || order.draggingGroup != null || deleting !== null;
  const [search, setSearch] = useState("");
  const [vendor, setVendor] = useState("");
  const vendors = Array.from(new Set(data.groups.map(group => group.vendor || "其他"))).sort((a, b) => {
    if (a === "其他") return 1;
    if (b === "其他") return -1;
    return a.localeCompare(b, "zh-CN");
  });
  const query = search.trim().toLowerCase();
  const filtered = data.groups.filter(group =>
    (!vendor || (group.vendor || "其他") === vendor) &&
    [group.name, group.vendor, ...group.routes.map(route => route.model)].join(" ").toLowerCase().includes(query),
  );
  const sections = vendors.map(name => ({
    name,
    groups: filtered.filter(group => (group.vendor || "其他") === name),
  })).filter(section => section.groups.length > 0);
  const selectedGroups = data.groups.filter(group => selected.has(group.id));
  function toggleSelection(ids: number[], checked: boolean) {
    setSelected(previous => {
      const next = new Set(previous);
      for (const id of ids) { if (checked) next.add(id); else next.delete(id); }
      return next;
    });
  }
  async function deleteModels() {
    if (!deleting || deleteBusy) return;
    setDeleteBusy(true); setDeleteResult(null);
    const removed: number[] = [];
    const failed: { id: number; name: string; error: string }[] = [];
    let refreshError = "";
    try {
      for (const group of deleting.groups) {
        try { await api(`/groups/${group.id}`, "DELETE"); removed.push(group.id); }
        catch (error) { failed.push({ id: group.id, name: group.name, error: (error as Error).message }); }
      }
      setSelected(previous => new Set([...previous].filter(id => !removed.includes(id)).concat(failed.map(item => item.id))));
      try { await refresh(); } catch (error) { refreshError = `列表刷新失败，请点击刷新查看最新结果：${(error as Error).message}`; }
      setDeleteResult({ removed: removed.length, failed, refreshError });
      setDeleting(null);
    } finally { setDeleteBusy(false); }
  }
  const deleteNotice = deleteResult && <div className="panel model-delete-result" role={deleteResult.failed.length || deleteResult.refreshError ? "alert" : "status"}>
    <p>已删除 {deleteResult.removed} 个模型，失败 {deleteResult.failed.length} 个。</p>
    {deleteResult.failed.length > 0 && <ul className="channel-batch-targets">{deleteResult.failed.map(item => <li key={item.id}>{item.name}：{item.error}</li>)}</ul>}
    {deleteResult.refreshError && <p className="form-error">{deleteResult.refreshError}</p>}
  </div>;
  if (!data.groups.length)
    return (
      <>
        {deleteNotice}
        <div className="panel">
          <Empty
            title="为客户端配置一个统一模型"
            icon={<Workflow size={25} aria-hidden="true" />}
            detail="将统一模型名映射到上游模型，默认手动选择当前使用的路由。"
          />
        </div>
      </>
    );
  return (
    <>
      {deleteNotice}
      <div className="list-toolbar model-toolbar">
        <div className="search"><Search size={16} /><input aria-label="搜索模型" placeholder="搜索模型或上游模型名称" value={search} onChange={event => setSearch(event.target.value)} /></div>
        <span>{filtered.length} / {data.groups.length} 个模型</span>
      </div>
      {routing.error && <p className="form-error" role="alert">运行状态刷新失败，以下可能为上次结果：{routing.error}</p>}
      <div className="vendor-filters" aria-label="模型厂商分类">
        {["", ...vendors].map(name => (
          <button key={name || "all"} className={vendor === name ? "selected" : ""} aria-pressed={vendor === name} onClick={() => setVendor(name)}>
            {name || "全部"}<span>{name ? data.groups.filter(group => (group.vendor || "其他") === name).length : data.groups.length}</span>
          </button>
        ))}
      </div>
      {selectedGroups.length > 0 && <div className="panel model-batch-toolbar" role="group" aria-label="模型批量操作">
        <label className="checkbox"><ChannelSelection label="选择当前筛选的全部模型" selected={filtered.filter(group => selected.has(group.id)).length} total={filtered.length} disabled={actionsBusy} toggle={checked => toggleSelection(filtered.map(group => group.id), checked)} />全选当前筛选结果</label>
        <span>已选 {selectedGroups.length} 个模型</span>
        <button className="secondary danger" disabled={actionsBusy || !selectedGroups.length} onClick={() => setDeleting({ groups: selectedGroups, all: false })}><Trash2 size={14} />删除所选（{selectedGroups.length}）</button>
        <button className="text-button" disabled={actionsBusy || !selectedGroups.length} onClick={() => setSelected(new Set())}>取消选择</button>
        <button className="secondary danger" disabled={actionsBusy} onClick={() => setDeleting({ groups: [...data.groups], all: true })}><Trash2 size={14} />清空全部模型（{data.groups.length}）</button>
      </div>}
      {sections.map(section => (
        <section className="model-vendor-group" key={section.name} aria-label={`${section.name}模型`}>
          <div className="section-heading"><h2>{section.name}</h2><span>{section.groups.length} 个模型</span></div>
          <div className="model-list">
      {section.groups.map((group) => {
        const usable = group.routes.some(route => (group.strategy !== "manual" || route.id === group.active_route_id) && channelEnabled(data.channels.find(row => row.channel.id === route.channel_id)));
        return (
        <section className="panel" key={group.id}>
          <div className="panel-heading">
            <div className="model-title">
              <ChannelSelection label={`选择模型 ${group.name}`} selected={selected.has(group.id) ? 1 : 0} total={1} disabled={actionsBusy} toggle={checked => toggleSelection([group.id], checked)} />
              <div className="model-icon">
                <RoutingStrategyIcon strategy={group.strategy} size={20} />
              </div>
              <div>
                <h2>{group.name}</h2>
                <p>{strategyNames[group.strategy]} · {group.routes.length} 条路由</p>
                <GroupRoutingSummary group={group} runtime={routing.states[group.id]} channels={data.channels} />
                {!usable && <span className="model-paused-state">已暂停 · 暂无可用路由</span>}
                <ModelPrice group={group} brief />
                <details className="model-details">
                  <summary>详细信息</summary>
                  <ModelPrice group={group} />
                  {group.routes.length > 1 && (
                    <p className="route-order-hint" id={`route-order-hint-${group.id}`}>
                      拖动左侧手柄排序并自动保存，也可聚焦手柄后使用上下方向键。
                      {group.strategy === "manual"
                        ? "手动模式只调整显示顺序，当前选中路由保持不变。"
                        : group.strategy === "weighted"
                          ? "权重按从上到下设为 N、N−1…1，越靠上分配的请求越多。"
                          : "越靠上越优先，优先级自动设为 0、1、2…"}
                    </p>
                  )}
                </details>
              </div>
            </div>
            <div className="inline-actions">
              <button className="secondary" disabled={actionsBusy} onClick={() => edit(group)}>
                <Pencil size={14} />
                编辑
              </button>
              <button
                className="icon-button danger"
                aria-label={`删除 ${group.name}`}
                disabled={actionsBusy}
                onClick={() => setDeleting({ groups: [group], all: false })}
              >
                <Trash2 size={16} />
              </button>
            </div>
          </div>
          {group.strategy === "manual" &&
            !group.routes.some(
              (route) => route.id === group.active_route_id,
            ) && (
              <p className="manual-route-hint">请选择当前使用的上游路由。</p>
            )}
          {order.savingGroup === group.id && (
            <p className="route-order-status" role="status"><LoaderCircle size={14} className="spin" />正在保存顺序…</p>
          )}
          {order.message?.groupID === group.id && (
            <p className={`route-order-status${order.message.error ? " form-error" : ""}`} role={order.message.error ? "alert" : "status"}>
              {order.message.text}
            </p>
          )}
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th className="route-order-cell">排序</th>
                  <th>渠道</th>
                  <th>倍率</th>
                  <th>上游模型</th>
                  <th>
                    {group.strategy === "manual"
                      ? "使用状态"
                      : group.strategy === "weighted"
                        ? "权重"
                        : "优先级"}
                  </th>
                  <th>协议</th>
                  <th className="route-remove-cell">操作</th>
                </tr>
              </thead>
              <tbody className={order.draggingGroup === group.id ? "route-sort-active" : undefined}>
                {group.routes.length === 0 && (
                  <tr>
                    <td colSpan={7}>
                      <p className="empty-routes">
                        暂无上游路由，编辑模型以添加其他渠道。
                      </p>
                    </td>
                  </tr>
                )}
                {group.routes.map((route) => {
                  const channel = data.channels.find(
                    (r) => r.channel.id === route.channel_id,
                  );
                  const ch = channel?.channel;
                  const enabled = channelEnabled(channel);
                  return (
                    <tr
                      key={route.id ?? `${route.channel_id}/${route.model}`}
                      data-order-group={group.id}
                      data-route-id={route.id}
                      className={["route-sortable-row", !enabled && "route-disabled", order.rowClass(group, route)].filter(Boolean).join(" ")}
                      style={order.rowStyle(group, route)}
                    >
                      <td className="route-order-cell">
                        <button
                          className="icon-button route-drag-handle"
                          {...order.handleProps(group, route)}
                          disabled={mutating || group.routes.length < 2 || !route.id}
                          aria-label={`调整 ${ch?.name || route.model} 顺序`}
                        >
                          <GripVertical size={18} />
                        </button>
                      </td>
                      <td>
                        {ch ? channelLabel(ch, data.accounts) : "渠道不存在"}
                        <RouteRoutingStatus runtime={routing.states[group.id]?.routes.find(row => row.route_id === route.id)} />
                        {channel?.monitoring_paused && <small className="table-sub channel-monitoring-paused">账户监控已停止，此路由暂停。</small>}
                        {ch && !ch.enabled && <small className="table-sub">{ch.auto_disabled_reason ? `自动停用：${ch.auto_disabled_reason}` : "已停用，不参与转发"}</small>}
                      </td>
                      <ChannelRate row={channel} />
                      <td>
                        <code>{route.model}</code>
                        <ModelPrice group={group} pricing={route.pricing} compact brief />
                        {(route.pricing || group.price_mode === "manual") && (
                          <details className="model-details route-price-details">
                            <summary>计价详情</summary>
                            <ModelPrice group={group} pricing={route.pricing} compact />
                          </details>
                        )}
                      </td>
                      <td>
                        {group.strategy === "manual" ? (
                          route.id === group.active_route_id ? (
                            <Badge status={enabled ? "ok" : "paused"}>
                              当前使用{enabled ? "" : " · 已暂停"}
                            </Badge>
                          ) : (
                            <Button
                              className="secondary"
                              disabled={!enabled || actionsBusy}
                              busy={switching === route.id}
                              onClick={async () => {
                                setSwitching(route.id!);
                                await run(
                                  () =>
                                    api(
                                      `/groups/${group.id}/selection`,
                                      "PATCH",
                                      { route_id: route.id },
                                    ),
                                  "已切换手动路由",
                                );
                                setSwitching(null);
                              }}
                            >
                              选择此路由
                            </Button>
                          )
                        ) : group.strategy === "weighted" ? (
                          route.weight
                        ) : (
                          route.priority
                        )}
                      </td>
                      <td>
                        {protocolsOf(ch)
                          .map((p) => protocolNames[p])
                          .join(" / ")}
                      </td>
                      <td className="route-remove-cell">
                        <Button
                          className="text-button danger"
                          aria-label={`从 ${group.name} 移除 ${ch?.name || "已删除渠道"} 的 ${route.model} 路由`}
                          disabled={!route.id || actionsBusy}
                          busy={removing === route.id}
                          onClick={async () => {
                            const consequence = group.routes.length === 1
                              ? "移除最后一条路由后，将保留模型配置，暂时无法转发请求。"
                              : group.strategy === "manual" && route.id === group.active_route_id
                                ? "移除当前使用的路由后，需要重新选择路由才能转发。"
                                : "";
                            if (!confirm(`从模型“${group.name}”移除渠道“${ch ? channelLabel(ch, data.accounts) : "渠道不存在"}”的路由“${route.model}”？${consequence}`)) return;
                            setRemoving(route.id!);
                            try {
                              await run(
                                () => api(`/groups/${group.id}/routes/${route.id}`, "DELETE"),
                                "已从当前模型移除路由",
                              );
                            } finally {
                              setRemoving(null);
                            }
                          }}
                        >
                          {removing !== route.id && <Trash2 size={15} />}
                          移除
                        </Button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      );})}
          </div>
        </section>
      ))}
      {filtered.length === 0 && <div className="panel"><Empty icon={<SearchX size={25} aria-hidden="true" />} title="没有匹配的模型" detail="调整厂商分类或搜索名称后重试。" /></div>}
      {deleting && <Dialog title={deleting.all ? "清空全部模型" : "删除模型"} close={() => { if (!deleteBusy) setDeleting(null); }}>
        <p className="delete-description">确认删除以下 {deleting.groups.length} 个模型及其路由配置？删除后，这些模型将无法通过网关调用。</p>
        {deleting.all && <p className="form-error">清空范围包含全部模型，不受当前搜索或厂商筛选限制。</p>}
        <ul className="channel-batch-targets">{deleting.groups.map(group => <li key={group.id}>{group.name}</li>)}</ul>
        <p className="field-hint">渠道配置、渠道保存的模型列表、近期请求及用量统计会保留。</p>
        <div className="form-footer">
          <button className="secondary" disabled={deleteBusy} onClick={() => setDeleting(null)}>取消</button>
          <Button className="destructive" busy={deleteBusy} onClick={() => void deleteModels()}>确认删除 {deleting.groups.length} 个模型</Button>
        </div>
      </Dialog>}
    </>
  );
}
function GroupForm({
  group,
  channels,
  accounts,
  close,
  saved,
}: {
  group?: Group;
  channels: ChannelRow[];
  accounts: AccountRow[];
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [name, setName] = useState(group?.name || "");
  const [vendor, setVendor] = useState(group?.vendor || "");
  const [priceMode, setPriceMode] = useState(group?.price_mode || "auto");
  const [priceModel, setPriceModel] = useState(group?.price_model || "");
  const [strategy, setStrategy] = useState(group?.strategy || "manual");
  const [relayConfig, setRelayConfig] = useState(group?.relay_config || { member_max_attempts: 2, member_retry_interval_seconds: 3, member_cooldown_seconds: 60, member_affinity_seconds: 300 });
  const [activeRouteIndex, setActiveRouteIndex] = useState(() =>
    Math.max(
      0,
      group?.routes.findIndex((route) => route.id === group.active_route_id) ??
        0,
    ),
  );
  const [inputPrice, setInputPrice] = useState(
    (group?.input_price_micros || 0) / 1e6,
  );
  const [outputPrice, setOutputPrice] = useState(
    (group?.output_price_micros || 0) / 1e6,
  );
  const [cacheReadPrice, setCacheReadPrice] = useState(group?.pricing?.cache_read == null ? "" : String(group.pricing.cache_read / 1e6));
  const [cacheWritePrice, setCacheWritePrice] = useState(group?.pricing?.cache_write == null ? "" : String(group.pricing.cache_write / 1e6));
  const [cacheWrite1hPrice, setCacheWrite1hPrice] = useState(group?.pricing?.cache_write_1h == null ? "" : String(group.pricing.cache_write_1h / 1e6));
  const [routes, setRoutes] = useState<Route[]>(
    group?.routes || [
      {
        channel_id: channels.find(channelEnabled)?.channel.id || 0,
        model: "",
        weight: 1,
        priority: 0,
      },
    ],
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const setRoute = (index: number, key: keyof Route, value: string | number) =>
    setRoutes((previous) =>
      previous.map((r, i) => (i === index ? { ...r, [key]: value } : r)),
    );
  return (
    <Dialog title={group ? "编辑模型路由" : "添加模型路由"} close={close}>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            await api(
              group ? `/groups/${group.id}` : "/groups",
              group ? "PUT" : "POST",
              {
                name,
                vendor,
                price_mode: priceMode,
                price_model: priceModel,
                strategy,
                relay_config: relayConfig,
                input_price_micros: Math.round(inputPrice * 1e6),
                output_price_micros: Math.round(outputPrice * 1e6),
                pricing: {
                  cache_read: cacheReadPrice === "" ? null : Math.round(Number(cacheReadPrice) * 1e6),
                  cache_write: cacheWritePrice === "" ? null : Math.round(Number(cacheWritePrice) * 1e6),
                  cache_write_1h: cacheWrite1hPrice === "" ? null : Math.round(Number(cacheWrite1hPrice) * 1e6),
                },
                routes,
                ...(strategy === "manual"
                  ? { active_route_index: activeRouteIndex }
                  : {}),
              },
            );
            await saved();
            close();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="form-grid">
          <Field label="客户端模型名">
            <input
              autoFocus
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如 my-assistant"
            />
          </Field>
          <Field label="调度策略">
            <select
              value={strategy}
              onChange={(e) => setStrategy(e.target.value)}
            >
              {Object.entries(strategyNames).map(([id, name]) => (
                <option key={id} value={id}>
                  {name}
                </option>
              ))}
            </select>
          </Field>
          {strategy === "priority" && <fieldset className="full group-relay-config"><legend>故障转移设置</legend><div className="form-grid">
            <Field label="单成员总尝试次数" hint="包含首次请求；耗尽后再切换其他成员。"><input type="number" required min={1} max={32} value={relayConfig.member_max_attempts} onChange={event => setRelayConfig({ ...relayConfig, member_max_attempts: Number(event.target.value) })} /></Field>
            <Field label="同成员重试间隔（秒）" hint="等待和每次尝试都计入整次请求预算。"><input type="number" required min={1} max={3600} value={relayConfig.member_retry_interval_seconds} onChange={event => setRelayConfig({ ...relayConfig, member_retry_interval_seconds: Number(event.target.value) })} /></Field>
            <Field label="成员失败冷却（秒）" hint="尝试耗尽后暂时跳过该成员。"><input type="number" required min={1} max={3600} value={relayConfig.member_cooldown_seconds} onChange={event => setRelayConfig({ ...relayConfig, member_cooldown_seconds: Number(event.target.value) })} /></Field>
            <Field label="切换后亲和时间（秒）" hint="备用成员成功后保持使用；0 表示关闭。"><input type="number" required min={0} max={86400} value={relayConfig.member_affinity_seconds} onChange={event => setRelayConfig({ ...relayConfig, member_affinity_seconds: Number(event.target.value) })} /></Field>
          </div></fieldset>}
          <Field label="模型厂商" hint="可手动填写，留空时根据模型名称识别。">
            <input list="model-vendor-options" value={vendor} onChange={event => setVendor(event.target.value)} placeholder="自动识别" />
            <datalist id="model-vendor-options">
              {["OpenAI", "Anthropic", "Google", "DeepSeek", "xAI", "Moonshot AI", "智谱", "MiniMax", "阿里云", "Mistral", "Meta", "Cohere", "字节跳动", "百度", "腾讯", "阶跃星辰", "其他"].map(name => <option key={name} value={name} />)}
            </datalist>
          </Field>
          <Field label="计价方式">
            <select value={priceMode} onChange={event => setPriceMode(event.target.value === "manual" ? "manual" : "auto")}>
              <option value="auto">自动同步渠道报价／公开参考价</option>
              <option value="manual">手动设置</option>
            </select>
          </Field>
          {priceMode === "auto" && (
            <div className="full"><Field label="计价模型" hint="留空按模型名称或上游模型精确匹配，也可填写来源中的完整标识，例如 deepseek/deepseek-v4-pro。">
              <input value={priceModel} onChange={event => setPriceModel(event.target.value)} placeholder="自动匹配" />
            </Field></div>
          )}
          <Field label="输入价格（美元 / 百万 Token）">
            <input
              disabled={priceMode === "auto"}
              required
              type="number"
              min="0"
              step="0.000001"
              value={inputPrice}
              onChange={(e) => setInputPrice(Number(e.target.value))}
            />
          </Field>
          <Field label="输出价格（美元 / 百万 Token）">
            <input
              disabled={priceMode === "auto"}
              required
              type="number"
              min="0"
              step="0.000001"
              value={outputPrice}
              onChange={(e) => setOutputPrice(Number(e.target.value))}
            />
          </Field>
          <Field label="缓存读取价格（美元 / 百万 Token）" hint="留空表示未知；0 表示免费。">
            <input disabled={priceMode === "auto"} type="number" min="0" max="1000000" step="0.000001" value={cacheReadPrice} placeholder="未提供" onChange={(event) => setCacheReadPrice(event.target.value)} />
          </Field>
          <Field label="缓存写入 5分钟（美元 / 百万 Token）" hint="用于默认缓存写入。">
            <input disabled={priceMode === "auto"} type="number" min="0" max="1000000" step="0.000001" value={cacheWritePrice} placeholder="未提供" onChange={(event) => setCacheWritePrice(event.target.value)} />
          </Field>
          <Field label="缓存写入 1小时（美元 / 百万 Token）" hint="上游报告 1 小时缓存写入时使用。">
            <input disabled={priceMode === "auto"} type="number" min="0" max="1000000" step="0.000001" value={cacheWrite1hPrice} placeholder="未提供" onChange={(event) => setCacheWrite1hPrice(event.target.value)} />
          </Field>
        </div>
        <div className="route-editor-heading">
          <h3>上游路由</h3>
          <button
            className="text-button"
            type="button"
            onClick={() =>
              setRoutes((previous) => [
                ...previous,
                {
                  channel_id: channels.find(channelEnabled)?.channel.id || 0,
                  model: "",
                  weight: 1,
                  priority: previous.length,
                },
              ])
            }
          >
            <Plus size={15} />
            添加
          </button>
        </div>
        {routes.map((route, index) => (
          <div className="route-editor" key={index}>
            <Field label="渠道">
              <select
                required
                value={route.channel_id || ""}
                onChange={(e) =>
                  setRoute(index, "channel_id", Number(e.target.value))
                }
              >
                <option value="" disabled>
                  选择渠道
                </option>
                {channels.map((row) => (
                  <option key={row.channel.id} value={row.channel.id} disabled={!channelEnabled(row)}>
                    {channelLabel(row.channel, accounts)}
                    {row.monitoring_paused ? "（监控已停止）" : !row.channel.enabled ? "（已停用）" : ""}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="上游模型">
              <input
                required
                list={`channel-models-${index}`}
                value={route.model}
                onChange={(e) => setRoute(index, "model", e.target.value)}
              />
              <datalist id={`channel-models-${index}`}>
                {channelModelNames(channels.find((c) => c.channel.id === route.channel_id)?.channel.models).map(
                  (model) => <option key={model} value={model} />,
                )}
              </datalist>
            </Field>
            {strategy === "manual" ? (
              <label className="checkbox manual-route-choice">
                <input
                  type="radio"
                  name="manual-route"
                  aria-label={`使用第 ${index + 1} 条路由`}
                  checked={activeRouteIndex === index}
                  disabled={!channelEnabled(channels.find((row) => row.channel.id === route.channel_id))}
                  onChange={() => setActiveRouteIndex(index)}
                />
                当前使用
              </label>
            ) : (
              <Field label={strategy === "weighted" ? "权重" : "优先级"}>
                <input
                  required
                  type="number"
                  min={strategy === "weighted" ? 1 : 0}
                  max={strategy === "weighted" ? 1000 : 10000}
                  value={
                    strategy === "weighted" ? route.weight : route.priority
                  }
                  onChange={(e) =>
                    setRoute(
                      index,
                      strategy === "weighted" ? "weight" : "priority",
                      Number(e.target.value),
                    )
                  }
                />
              </Field>
            )}
            <button
              className="icon-button danger"
              aria-label="移除路由"
              disabled={routes.length === 1}
              type="button"
              onClick={() => {
                setRoutes((previous) => previous.filter((_, i) => i !== index));
                setActiveRouteIndex((previous) =>
                  previous === index
                    ? 0
                    : previous > index
                      ? previous - 1
                      : previous,
                );
              }}
            >
              <Trash2 size={16} />
            </button>
          </div>
        ))}
        <p className="field-hint">
          {strategy === "manual"
            ? "固定使用选中的上游路由，可在模型列表中手动切换。上游故障时需选择其他路由。"
            : strategy === "weighted"
              ? "健康渠道按权重分摊请求。例如权重 3 和 1 对应约 75% 与 25%。"
              : "优先级数字越小越优先。临时失败先重试当前成员，耗尽后再切换；备用成功后保持亲和，期满只允许单个请求探测恢复。"}
        </p>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-footer">
          <button className="secondary" type="button" onClick={close}>
            取消
          </button>
          <Button
            busy={busy}
            className="primary"
            type="submit"
            disabled={!channels.length}
          >
            保存路由
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function Keys({
  data,
  run,
}: {
  data: Data;
  run: (f: () => Promise<unknown>, s?: string) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<Key | null>(null);
  return (
    <>
      <div className="panel key-create">
        <div>
          <h2 className="icon-heading"><KeyRound size={18} aria-hidden="true" />创建网关密钥</h2>
          <p>客户端使用网关密钥调用已配置的统一模型。</p>
        </div>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            await run(async () => {
              const result = await api<{ token: string }>("/keys", "POST", {
                name,
              });
              setSecret(result.token);
              setName("");
            }, "密钥已创建");
            setBusy(false);
          }}
        >
          <input
            aria-label="密钥名称"
            required
            maxLength={100}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如 Claude Code / 个人应用"
          />
          <Button className="primary" busy={busy} type="submit">
            <Plus size={16} />
            创建密钥
          </Button>
        </form>
      </div>
      <div className="panel gateway-key-panel">
        <div className="panel-heading">
          <h2 className="icon-heading"><ShieldCheck size={18} aria-hidden="true" />访问凭据</h2>
          <div className="inline-actions gateway-key-endpoint">
            <code>{location.origin}/v1</code>
            <CopyButton text={`${location.origin}/v1`} label="复制地址" />
          </div>
        </div>
        {!data.keys.length ? (
          <Empty title="尚未创建网关密钥" icon={<KeyRound size={25} aria-hidden="true" />} />
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>名称</th>
                  <th>密钥前缀</th>
                  <th>创建时间</th>
                  <th>用量与限制</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {data.keys.map((key) => (
                  <tr key={key.id}>
                    <td>
                      <strong>{key.name}</strong>
                    </td>
                    <td>
                      <code>{key.prefix}••••••••</code>
                    </td>
                    <td>{date(key.created_at)}</td>
                    <td>
                      <strong>${money((key.used_micros || 0) / 1e6)} / {key.budget_micros == null ? "不限预算" : `$${money(key.budget_micros / 1e6)}`}</strong>
                      <small className="table-sub">{key.enabled ? key.expires_at && new Date(key.expires_at).getTime() <= Date.now() ? "已到期" : "已启用" : "已停用"}{key.expires_at && ` · 到期 ${date(key.expires_at)}`}</small>
                      <small className="table-sub">{key.allowed_models?.length ? `可用模型：${key.allowed_models.join("、")}` : "全部模型"}</small>
                    </td>
                    <td>
                      <div className="inline-actions">
                        <button className="text-button" onClick={() => setEditing(key)}>编辑限制</button>
                        <span
                          title={
                            key.can_copy
                              ? undefined
                              : "旧密钥仅保存摘要，无法恢复完整内容，请创建新密钥"
                          }
                        >
                          <CopyButton
                            label="复制密钥"
                            disabled={!key.can_copy}
                            text={async () => {
                              const result = await api<{ token: string }>(
                                `/keys/${key.id}/copy`,
                                "POST",
                              );
                              return result.token;
                            }}
                          />
                        </span>
                        <button
                          className="text-button danger"
                          onClick={() => {
                            if (
                              confirm(
                                `撤销密钥“${key.name}”？使用它的客户端将无法继续访问。`,
                              )
                            )
                              run(() => api(`/keys/${key.id}`, "DELETE"));
                          }}
                        >
                          撤销
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {editing && <KeyControls item={editing} availableModels={data.groups.map(group => group.name)} close={() => setEditing(null)} saved={async () => { await run(async () => {}); }} />}
      {secret && (
        <Dialog title="保存你的网关密钥" close={() => setSecret("")}>
          <p className="field-hint">
            请保存到客户端配置中，之后也可在密钥列表中复制。
          </p>
          <div className="secret-box">
            <textarea aria-label="完整网关密钥" readOnly value={secret} />
            <CopyButton text={secret} label="复制密钥" />
          </div>
          <div className="form-footer">
            <button className="primary" onClick={() => setSecret("")}>
              已保存
            </button>
          </div>
        </Dialog>
      )}
    </>
  );
}
function KeyControls({ item, availableModels, close, saved }: { item: Key; availableModels: string[]; close: () => void; saved: () => Promise<void> }) {
  const [name, setName] = useState(item.name);
  const [enabled, setEnabled] = useState(item.enabled);
  const [models, setModels] = useState(item.allowed_models || []);
  const [expiry, setExpiry] = useState(item.expires_at ? new Date(new Date(item.expires_at).getTime() + 8 * 3600000).toISOString().slice(0, 10) : "");
  const [budget, setBudget] = useState(item.budget_micros == null ? "" : (item.budget_micros / 1e6).toString());
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  return <Dialog title="编辑网关密钥限制" close={close}><form onSubmit={async event => {
    event.preventDefault(); setError(""); setBusy(true);
    try {
      let amount: number | null = null;
      if (budget.trim() !== "") {
        if (!/^\d+(\.\d{1,6})?$/.test(budget.trim())) throw new Error("预算须为非负美元金额，最多 6 位小数。");
        const [whole, fraction = ""] = budget.trim().split(".");
        const micros = BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, "0"));
        if (micros > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error("预算金额过大。");
        amount = Number(micros);
      }
      await api(`/keys/${item.id}`, "PUT", { name, enabled, allowed_models: models, expires_at: expiry ? new Date(`${expiry}T23:59:59.999+08:00`).toISOString() : null, budget_micros: amount });
      await saved(); close();
    } catch (error) { setError((error as Error).message); } finally { setBusy(false); }
  }}><div className="form-grid">
    <Field label="密钥名称"><input required maxLength={100} value={name} onChange={event => setName(event.target.value)} /></Field>
    <Field label="累计预算上限（美元）" hint="留空不限，0 表示禁止新请求；修改上限不会清零累计费用。"><input inputMode="decimal" value={budget} onChange={event => setBudget(event.target.value)} /></Field>
    <Field label="有效期截至（北京时间）" hint="包含所选日期；留空表示不过期。"><input type="date" value={expiry} onChange={event => setExpiry(event.target.value)} /></Field>
    <label className="checkbox"><input type="checkbox" checked={enabled} onChange={event => setEnabled(event.target.checked)} />启用此密钥</label>
    <div className="full"><KeyModelPicker available={availableModels} selected={models} change={setModels} disabled={busy} /></div>
  </div><p className="field-hint">达到预算后阻止新请求，已在进行中的请求仍可产生费用；费用以网关记录的估算为准。</p>{error && <p className="form-error" role="alert">{error}</p>}<div className="form-footer"><button type="button" className="secondary" onClick={close}>取消</button><Button className="primary" busy={busy} type="submit">保存限制</Button></div></form></Dialog>;
}
function Requests({ rows, channels, small }: { rows: RequestLog[]; channels: ChannelRow[]; small?: boolean }) {
  return <RequestList rows={rows} channels={channels} small={small} dialog={Dialog} />;
}
function SettingsForm({
  settings,
  run,
}: {
  settings: Settings;
  run: (f: () => Promise<unknown>, s?: string) => Promise<void>;
}) {
  const [form, setForm] = useState({
    ...settings,
  });
  const [busy, setBusy] = useState(false);
  const set = (key: string, value: unknown) =>
    setForm((previous) => ({ ...previous, [key]: value }));
  const setRuntime = (key: keyof RuntimeOptions, value: unknown) => setForm(previous => ({ ...previous, runtime: { ...previous.runtime, [key]: value } }));
  return (
    <form
      className="panel settings-panel runtime-settings-panel"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        await run(() => api("/settings", "PUT", form), "系统设置已保存");
        setBusy(false);
      }}
    >
      <div className="panel-heading">
        <div>
          <h2 className="icon-heading"><Settings2 size={18} aria-hidden="true" />告警阈值与运行设置</h2>
          <p>调整告警门槛、任务周期与历史保留时间。</p>
        </div>
      </div>
      <div className="settings-fields">
        <details className="runtime-options"><summary><BellRing size={15} aria-hidden="true" />告警阈值与重试默认值</summary>
          <div className="form-grid">{([
            ["rate_change_percent", "倍率变动通知门槛（%）", 0, 10000], ["subscription_low_percent", "订阅剩余比例阈值（%）", 1, 100], ["subscription_expiry_hours", "订阅临近到期（小时）", 1, 8760], ["notification_retries", "新通知渠道默认尝试次数", 1, 30],
          ] as const).map(([key, label, min, max]) => <Field label={label} key={key}><input required type="number" min={min} max={max} step={key === "rate_change_percent" ? "any" : "1"} value={form.runtime[key]} onChange={event => setRuntime(key, Number(event.target.value))} /></Field>)}</div>
          <p className="field-hint">倍率门槛仅影响推送，真实变化仍记入本地历史；分组新增和移除始终符合倍率通知门槛。</p>
        </details>
        <details className="runtime-options"><summary><Timer size={15} aria-hidden="true" />转发超时与采集并发</summary><div className="form-grid">{([
          ["first_output_seconds", "等待首个有效输出（秒）", 1, 3600], ["stream_idle_seconds", "流无数据超时（秒）", 1, 3600], ["request_timeout_seconds", "整次请求总时间上限（秒）", 1, 10800], ["max_attempts", "整次请求最多尝试次数", 1, 32], ["monitor_concurrency", "账户采集并发数", 1, 8],
        ] as const).map(([key, label, min, max]) => <Field label={label} key={key}><input required type="number" min={min} max={max} value={form.runtime[key]} onChange={event => setRuntime(key, Number(event.target.value))} /></Field>)}</div><p className="field-hint">流式开始输出后按无数据超时和整次请求总时间上限控制；客户端取消请求后停止后续尝试。</p></details>
        <details className="runtime-options"><summary><RefreshCw size={15} aria-hidden="true" />定时价格同步与模型发现</summary><div className="form-grid">
          <Field label="价格同步周期（小时）" hint="0 表示关闭，手动价格保持不变。"><input required type="number" min="0" max="720" value={form.runtime.price_sync_hours} onChange={event => setRuntime("price_sync_hours", Number(event.target.value))} /></Field>
          <Field label="模型发现周期（小时）" hint="0 表示关闭，发现结果在渠道中预览，确认同步后修改已选模型。"><input required type="number" min="0" max="720" value={form.runtime.model_sync_hours} onChange={event => setRuntime("model_sync_hours", Number(event.target.value))} /></Field>
        </div>{settings.sync_tasks?.map(task => <p className="field-hint" key={task.name}>{task.name === "prices" ? "价格同步" : "模型发现"}：{task.started_at && !task.finished_at ? "正在运行" : task.summary || "尚未执行"} · 上次成功 {date(task.last_success_at)}{task.error && ` · ${task.error}`}</p>)}</details>
        <div className="form-grid">
          <Field label="同类告警冷却（分钟）">
            <input
              required
              type="number"
              min="1"
              max="10080"
              value={form.alert_cooldown_minutes}
              onChange={(e) =>
                set("alert_cooldown_minutes", Number(e.target.value))
              }
            />
          </Field>
          <Field label="监控快照与告警保留天数">
            <input
              required
              type="number"
              min="1"
              max="365"
              value={form.retention_days}
              onChange={(e) => set("retention_days", Number(e.target.value))}
            />
          </Field>
          <Field label="用量统计保留天数" hint="按小时、日期及渠道/模型汇总请求量、Token、费用与性能，默认 365 天。">
            <input required type="number" min="1" max="365" value={form.request_summary_retention_days} onChange={event => set("request_summary_retention_days", Number(event.target.value))} />
          </Field>
        </div>
        <div className="settings-notes">
          <p className="field-hint">用量统计按北京时间自然日保留，包含当天，每小时自动清理。缩短周期后，超期统计会自动删除。</p>
          <p className="field-hint">近期请求和正文仅缓存在内存中，最多 50 条已结束请求、32 MiB，重启后清空；进行中请求另计。</p>
          <p className="field-hint">通知失败按各渠道设置独立重试；修改默认值仅影响新建渠道。</p>
        </div>
      </div>
      <div className="form-footer">
        <Button busy={busy} className="primary" type="submit">
          保存设置
        </Button>
      </div>
    </form>
  );
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
