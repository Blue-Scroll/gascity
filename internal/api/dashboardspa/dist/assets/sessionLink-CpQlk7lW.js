function r(i,e,n,t){const s=new URLSearchParams;e&&s.set("back",e),n&&s.set("label",n),t&&s.set("tmux",t);const o=s.toString();return`/session/${encodeURIComponent(i)}${o?`?${o}`:""}`}export{r as s};
