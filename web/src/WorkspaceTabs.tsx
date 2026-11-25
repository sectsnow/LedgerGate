export default function WorkspaceTabs({prefix,tabs,selected,label}:{prefix:string;tabs:readonly {id:string;name:string}[];selected:string;label:string}) {
  return <nav className="workspace-tabs" aria-label={label}>{tabs.map(tab=><a key={tab.id} href={`#${prefix}/${tab.id}`} aria-current={selected===tab.id ? "page" : undefined} className={selected===tab.id ? "selected" : ""}>{tab.name}</a>)}</nav>;
}
