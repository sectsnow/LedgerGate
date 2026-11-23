import type { BackendBuild } from "./api";

export function buildDescription(build?: BackendBuild | null): string {
  if (!build) return "后端版本尚未确认";
  return `后端 v${build.version}\n提交：${build.revision || "未提供"}\n构建时间：${build.built_at || "未注入"}${build.modified ? "\n包含未提交的源码修改" : ""}`;
}

export default function BuildInfoPanel({ build, frontend }: { build?: BackendBuild | null; frontend: string }) {
  return <section className="panel settings-panel build-info-panel">
    <div className="panel-heading"><div><h2>运行版本</h2><p>核对当前运行版本、源码提交与构建时间。</p></div></div>
    <div className="build-info-content">
      <dl className="build-info-grid">
        <div><dt>后端版本</dt><dd>{build ? `v${build.version}` : "尚未确认"}</dd></div>
        <div><dt>前端版本</dt><dd>v{frontend}</dd></div>
        <div><dt>后端提交</dt><dd><code>{build?.revision || "未提供"}</code></dd></div>
        <div><dt>构建时间</dt><dd>{build?.built_at ? new Date(build.built_at).toLocaleString("zh-CN", { hour12: false }) : "未注入"}</dd></div>
      </dl>
      {build?.modified && <p className="field-hint">此构建包含未提交的源码修改，提交号不能完整代表其代码内容。</p>}
      {build && build.version !== frontend && <p className="form-error">前后端版本不一致，请刷新页面并核对部署。</p>}
    </div>
  </section>;
}
