import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, type Data } from "./api";
import { workspacePaths, workspacePollInterval, workspaceResources } from "./workspaceNavigation";

export default function useWorkspaceData(page: string, authenticated: boolean | null, onError: (message:string)=>void) {
  const [cache,setCache]=useState<Partial<Data>>({});
  const [error,setError]=useState("");
  const saved=useRef<Partial<Data>>({});
  const context=useRef({page,authenticated});context.current={page,authenticated};
  const revision=useRef(0), controller=useRef<AbortController|null>(null);
  const load=useCallback(async(mode:"enter"|"poll"|"force")=>{
    if (!context.current.authenticated || mode==="poll" && controller.current) return;
    const active=context.current.page;
    const resources=workspaceResources(active).filter(key=>{
      if(mode==="force")return true;
      if(mode==="enter")return saved.current[key]===undefined || key!=="settings" && key!=="build";
      return key!=="settings" && key!=="build";
    });
    if (!resources.length) return;
    if(mode!=="poll")setError("");
    controller.current?.abort();
    const request=new AbortController();controller.current=request;
    const order=++revision.current;
    try {
      const values=await Promise.all(resources.map(async key=>{
        try {return [key,await api<Data[typeof key]>(workspacePaths[key],"GET",undefined,request.signal)] as const;}
        catch(error) {if(key==="build" && !request.signal.aborted)return [key,null] as const;throw error;}
      }));
      if (request.signal.aborted || order!==revision.current || context.current.page!==active || !context.current.authenticated) return;
      saved.current={...saved.current,...Object.fromEntries(values)};
      setCache(saved.current);
      setError("");
    } catch(error) {
      if(request.signal.aborted || order!==revision.current || context.current.page!==active || !context.current.authenticated)return;
      setError((error as Error).message);
      request.abort();throw error;
    } finally {if(order===revision.current)controller.current=null;}
  },[]);
  const refresh=useCallback(()=>load("force"),[load]);
  const clear=useCallback(()=>{++revision.current;controller.current?.abort();controller.current=null;saved.current={};setCache({});setError("");},[]);
  useEffect(()=>{
    if (!authenticated)return;
    const read=(mode:"enter"|"poll")=>void load(mode).catch(error=>{if(error.name!=="AbortError")onError(error.message);});
    read("enter");
    const interval=workspacePollInterval(page);
    const timer=interval ? window.setInterval(()=>{if(!document.hidden)read("poll");},interval) : undefined;
    return ()=>{if(timer!==undefined)window.clearInterval(timer);++revision.current;controller.current?.abort();controller.current=null;};
  },[page,authenticated,load,onError]);
  const ready=workspaceResources(page).every(key=>cache[key]!==undefined) && !!cache.settings;
  // 非当前页面的资源不显示；真实页面必须等待其所需资源全部返回。
  const data=useMemo<Data|null>(()=>ready ? {
    accounts:cache.accounts??[],channels:cache.channels??[],groups:cache.groups??[],keys:cache.keys??[],requests:cache.requests??[],
    alerts:cache.alerts??{items:[],total:0,page:1,page_size:5,pending:0},
    rateChanges:{items:[],total:0,page:1,page_size:5,pending:0},announcements:{items:[],total:0,page:1,page_size:5,warnings:[]},
    settings:cache.settings!,build:cache.build,
    overview:cache.overview??{requests:0,errors:0,input_tokens:0,output_tokens:0,cost_micros:0,cost_unknown_requests:0,account_summaries:[],activity:[]},
  } : null,[cache,ready,page]);
  return {data,error,refresh,clear};
}
