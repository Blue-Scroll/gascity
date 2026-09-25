import{j as h}from"./index-iPNgFzC1.js";var c=function(i,e){return Object.defineProperty?Object.defineProperty(i,"raw",{value:e}):i.raw=e,i},a;(function(i){i[i.EOS=0]="EOS",i[i.Text=1]="Text",i[i.Incomplete=2]="Incomplete",i[i.ESC=3]="ESC",i[i.Unknown=4]="Unknown",i[i.SGR=5]="SGR",i[i.OSCURL=6]="OSCURL"})(a||(a={}));class p{constructor(){this.VERSION="6.0.6",this.setup_palettes(),this._use_classes=!1,this.bold=!1,this.faint=!1,this.italic=!1,this.underline=!1,this.fg=this.bg=null,this._buffer="",this._url_allowlist={http:1,https:1},this._escape_html=!0,this.boldStyle="font-weight:bold",this.faintStyle="opacity:0.7",this.italicStyle="font-style:italic",this.underlineStyle="text-decoration:underline"}set use_classes(e){this._use_classes=e}get use_classes(){return this._use_classes}set url_allowlist(e){this._url_allowlist=e}get url_allowlist(){return this._url_allowlist}set escape_html(e){this._escape_html=e}get escape_html(){return this._escape_html}set boldStyle(e){this._boldStyle=e}get boldStyle(){return this._boldStyle}set faintStyle(e){this._faintStyle=e}get faintStyle(){return this._faintStyle}set italicStyle(e){this._italicStyle=e}get italicStyle(){return this._italicStyle}set underlineStyle(e){this._underlineStyle=e}get underlineStyle(){return this._underlineStyle}setup_palettes(){this.ansi_colors=[[{rgb:[0,0,0],class_name:"ansi-black"},{rgb:[187,0,0],class_name:"ansi-red"},{rgb:[0,187,0],class_name:"ansi-green"},{rgb:[187,187,0],class_name:"ansi-yellow"},{rgb:[0,0,187],class_name:"ansi-blue"},{rgb:[187,0,187],class_name:"ansi-magenta"},{rgb:[0,187,187],class_name:"ansi-cyan"},{rgb:[255,255,255],class_name:"ansi-white"}],[{rgb:[85,85,85],class_name:"ansi-bright-black"},{rgb:[255,85,85],class_name:"ansi-bright-red"},{rgb:[0,255,0],class_name:"ansi-bright-green"},{rgb:[255,255,85],class_name:"ansi-bright-yellow"},{rgb:[85,85,255],class_name:"ansi-bright-blue"},{rgb:[255,85,255],class_name:"ansi-bright-magenta"},{rgb:[85,255,255],class_name:"ansi-bright-cyan"},{rgb:[255,255,255],class_name:"ansi-bright-white"}]],this.palette_256=[],this.ansi_colors.forEach(n=>{n.forEach(t=>{this.palette_256.push(t)})});let e=[0,95,135,175,215,255];for(let n=0;n<6;++n)for(let t=0;t<6;++t)for(let r=0;r<6;++r){let l={rgb:[e[n],e[t],e[r]],class_name:"truecolor"};this.palette_256.push(l)}let s=8;for(let n=0;n<24;++n,s+=10){let t={rgb:[s,s,s],class_name:"truecolor"};this.palette_256.push(t)}}escape_txt_for_html(e){return this._escape_html?e.replace(/[&<>"']/gm,s=>{if(s==="&")return"&amp;";if(s==="<")return"&lt;";if(s===">")return"&gt;";if(s==='"')return"&quot;";if(s==="'")return"&#x27;"}):e}append_buffer(e){var s=this._buffer+e;this._buffer=s}get_next_packet(){var e={kind:a.EOS,text:"",url:""},s=this._buffer.length;if(s==0)return e;var n=this._buffer.indexOf("\x1B");if(n==-1)return e.kind=a.Text,e.text=this._buffer,this._buffer="",e;if(n>0)return e.kind=a.Text,e.text=this._buffer.slice(0,n),this._buffer=this._buffer.slice(n),e;if(n==0){if(s<3)return e.kind=a.Incomplete,e;var t=this._buffer.charAt(1);if(t!="["&&t!="]"&&t!="(")return e.kind=a.ESC,e.text=this._buffer.slice(0,1),this._buffer=this._buffer.slice(1),e;if(t=="["){this._csi_regex||(this._csi_regex=b(g||(g=c([`
                        ^                           # beginning of line
                                                    #
                                                    # First attempt
                        (?:                         # legal sequence
                          \x1B[                      # CSI
                          ([<-?]?)              # private-mode char
                          ([d;]*)                    # any digits or semicolons
                          ([ -/]?               # an intermediate modifier
                          [@-~])                # the command
                        )
                        |                           # alternate (second attempt)
                        (?:                         # illegal sequence
                          \x1B[                      # CSI
                          [ -~]*                # anything legal
                          ([\0-:])              # anything illegal
                        )
                    `],[`
                        ^                           # beginning of line
                                                    #
                                                    # First attempt
                        (?:                         # legal sequence
                          \\x1b\\[                      # CSI
                          ([\\x3c-\\x3f]?)              # private-mode char
                          ([\\d;]*)                    # any digits or semicolons
                          ([\\x20-\\x2f]?               # an intermediate modifier
                          [\\x40-\\x7e])                # the command
                        )
                        |                           # alternate (second attempt)
                        (?:                         # illegal sequence
                          \\x1b\\[                      # CSI
                          [\\x20-\\x7e]*                # anything legal
                          ([\\x00-\\x1f:])              # anything illegal
                        )
                    `]))));let l=this._buffer.match(this._csi_regex);if(l===null)return e.kind=a.Incomplete,e;if(l[4])return e.kind=a.ESC,e.text=this._buffer.slice(0,1),this._buffer=this._buffer.slice(1),e;l[1]!=""||l[3]!="m"?e.kind=a.Unknown:e.kind=a.SGR,e.text=l[2];var r=l[0].length;return this._buffer=this._buffer.slice(r),e}else if(t=="]"){if(s<4)return e.kind=a.Incomplete,e;if(this._buffer.charAt(2)!="8"||this._buffer.charAt(3)!=";")return e.kind=a.ESC,e.text=this._buffer.slice(0,1),this._buffer=this._buffer.slice(1),e;this._osc_st||(this._osc_st=w(d||(d=c([`
                        (?:                         # legal sequence
                          (\x1B\\)                    # ESC                           |                           # alternate
                          (\x07)                      # BEL (what xterm did)
                        )
                        |                           # alternate (second attempt)
                        (                           # illegal sequence
                          [\0-]                 # anything illegal
                          |                           # alternate
                          [\b-]                 # anything illegal
                          |                           # alternate
                          [-]                 # anything illegal
                        )
                    `],[`
                        (?:                         # legal sequence
                          (\\x1b\\\\)                    # ESC \\
                          |                           # alternate
                          (\\x07)                      # BEL (what xterm did)
                        )
                        |                           # alternate (second attempt)
                        (                           # illegal sequence
                          [\\x00-\\x06]                 # anything illegal
                          |                           # alternate
                          [\\x08-\\x1a]                 # anything illegal
                          |                           # alternate
                          [\\x1c-\\x1f]                 # anything illegal
                        )
                    `])))),this._osc_st.lastIndex=0;{let u=this._osc_st.exec(this._buffer);if(u===null)return e.kind=a.Incomplete,e;if(u[3])return e.kind=a.ESC,e.text=this._buffer.slice(0,1),this._buffer=this._buffer.slice(1),e}{let u=this._osc_st.exec(this._buffer);if(u===null)return e.kind=a.Incomplete,e;if(u[3])return e.kind=a.ESC,e.text=this._buffer.slice(0,1),this._buffer=this._buffer.slice(1),e}this._osc_regex||(this._osc_regex=b(x||(x=c([`
                        ^                           # beginning of line
                                                    #
                        \x1B]8;                    # OSC Hyperlink
                        [ -:<-~]*       # params (excluding ;)
                        ;                           # end of params
                        ([!-~]{0,512})        # URL capture
                        (?:                         # ST
                          (?:\x1B\\)                  # ESC                           |                           # alternate
                          (?:\x07)                    # BEL (what xterm did)
                        )
                        ([ -~]+)              # TEXT capture
                        \x1B]8;;                   # OSC Hyperlink End
                        (?:                         # ST
                          (?:\x1B\\)                  # ESC                           |                           # alternate
                          (?:\x07)                    # BEL (what xterm did)
                        )
                    `],[`
                        ^                           # beginning of line
                                                    #
                        \\x1b\\]8;                    # OSC Hyperlink
                        [\\x20-\\x3a\\x3c-\\x7e]*       # params (excluding ;)
                        ;                           # end of params
                        ([\\x21-\\x7e]{0,512})        # URL capture
                        (?:                         # ST
                          (?:\\x1b\\\\)                  # ESC \\
                          |                           # alternate
                          (?:\\x07)                    # BEL (what xterm did)
                        )
                        ([\\x20-\\x7e]+)              # TEXT capture
                        \\x1b\\]8;;                   # OSC Hyperlink End
                        (?:                         # ST
                          (?:\\x1b\\\\)                  # ESC \\
                          |                           # alternate
                          (?:\\x07)                    # BEL (what xterm did)
                        )
                    `]))));let l=this._buffer.match(this._osc_regex);if(l===null)return e.kind=a.ESC,e.text=this._buffer.slice(0,1),this._buffer=this._buffer.slice(1),e;e.kind=a.OSCURL,e.url=l[1],e.text=l[2];var r=l[0].length;return this._buffer=this._buffer.slice(r),e}else if(t=="(")return e.kind=a.Unknown,this._buffer=this._buffer.slice(3),e}}ansi_to_html(e){this.append_buffer(e);for(var s=[];;){var n=this.get_next_packet();if(n.kind==a.EOS||n.kind==a.Incomplete)break;n.kind==a.ESC||n.kind==a.Unknown||(n.kind==a.Text?s.push(this.transform_to_html(this.with_state(n))):n.kind==a.SGR?this.process_ansi(n):n.kind==a.OSCURL&&s.push(this.process_hyperlink(n)))}return s.join("")}with_state(e){return{bold:this.bold,faint:this.faint,italic:this.italic,underline:this.underline,fg:this.fg,bg:this.bg,text:e.text}}process_ansi(e){let s=e.text.split(";");for(;s.length>0;){let n=s.shift(),t=parseInt(n,10);if(isNaN(t)||t===0)this.fg=null,this.bg=null,this.bold=!1,this.faint=!1,this.italic=!1,this.underline=!1;else if(t===1)this.bold=!0;else if(t===2)this.faint=!0;else if(t===3)this.italic=!0;else if(t===4)this.underline=!0;else if(t===21)this.bold=!1;else if(t===22)this.faint=!1,this.bold=!1;else if(t===23)this.italic=!1;else if(t===24)this.underline=!1;else if(t===39)this.fg=null;else if(t===49)this.bg=null;else if(t>=30&&t<38)this.fg=this.ansi_colors[0][t-30];else if(t>=40&&t<48)this.bg=this.ansi_colors[0][t-40];else if(t>=90&&t<98)this.fg=this.ansi_colors[1][t-90];else if(t>=100&&t<108)this.bg=this.ansi_colors[1][t-100];else if((t===38||t===48)&&s.length>0){let r=t===38,l=s.shift();if(l==="5"&&s.length>0){let f=parseInt(s.shift(),10);f>=0&&f<=255&&(r?this.fg=this.palette_256[f]:this.bg=this.palette_256[f])}if(l==="2"&&s.length>2){let f=parseInt(s.shift(),10),u=parseInt(s.shift(),10),o=parseInt(s.shift(),10);if(f>=0&&f<=255&&u>=0&&u<=255&&o>=0&&o<=255){let _={rgb:[f,u,o],class_name:"truecolor"};r?this.fg=_:this.bg=_}}}}}transform_to_html(e){let s=e.text;if(s.length===0||(s=this.escape_txt_for_html(s),!e.bold&&!e.italic&&!e.faint&&!e.underline&&e.fg===null&&e.bg===null))return s;let n=[],t=[],r=e.fg,l=e.bg;e.bold&&n.push(this._boldStyle),e.faint&&n.push(this._faintStyle),e.italic&&n.push(this._italicStyle),e.underline&&n.push(this._underlineStyle),this._use_classes?(r&&(r.class_name!=="truecolor"?t.push(`${r.class_name}-fg`):n.push(`color:rgb(${r.rgb.join(",")})`)),l&&(l.class_name!=="truecolor"?t.push(`${l.class_name}-bg`):n.push(`background-color:rgb(${l.rgb.join(",")})`))):(r&&n.push(`color:rgb(${r.rgb.join(",")})`),l&&n.push(`background-color:rgb(${l.rgb})`));let f="",u="";return t.length&&(f=` class="${t.join(" ")}"`),n.length&&(u=` style="${n.join(";")}"`),`<span${u}${f}>${s}</span>`}process_hyperlink(e){let s=e.url.split(":");return s.length<1||!this._url_allowlist[s[0]]?"":`<a href="${this.escape_txt_for_html(e.url)}">${this.escape_txt_for_html(e.text)}</a>`}}function b(i,...e){let s=i.raw[0],n=/^\s+|\s+\n|\s*#[\s\S]*?\n|\n/gm,t=s.replace(n,"");return new RegExp(t)}function w(i,...e){let s=i.raw[0],n=/^\s+|\s+\n|\s*#[\s\S]*?\n|\n/gm,t=s.replace(n,"");return new RegExp(t,"g")}var g,d,x;const C=/\x1b\][^\x07\x1b\x9c]*(?:\x07|\x1b\\|\x9c)/g,O=/\x1b\[[?0-9;]*[a-ln-zA-Z]/g,T=/\x1b(?!\[[?0-9;]*m)[@-Z\\-_]?/g,k=/[\x00-\x08\x0b\x0c\x0e-\x1a\x1c-\x1f\x7f-\x9f]/g;function m(i){return i.replace(C,"").replace(O,"").replace(T,"").replace(k,"")}const R=["color","backgroundColor"];function y(i){const e=i.getAttribute("style");if(!e)return;const s={};for(const n of e.split(";")){const t=n.indexOf(":");if(t<0)continue;const r=n.slice(0,t).trim().toLowerCase(),l=n.slice(t+1).trim();if(!l)continue;const f=r==="color"?"color":r==="background-color"?"backgroundColor":null;f&&R.includes(f)&&(s[f]=l)}return Object.keys(s).length>0?s:void 0}function S(i,e){if(i.nodeType===3)return i.textContent??"";if(i.nodeType!==1)return null;const s=i,n=Array.from(s.childNodes).map((r,l)=>S(r,`${e}-${l}`)),t=s.tagName.toLowerCase();return t==="br"?h.jsx("br",{},e):t!=="span"?h.jsx("span",{children:n},e):h.jsx("span",{className:s.getAttribute("class")??void 0,style:y(s),children:n},e)}function E(i,e,s){if(i.nodeType===3){const u=i.textContent??"";u&&s.push({...e,text:u});return}if(i.nodeType!==1)return;const n=i,t={text:""},r=n.getAttribute("class"),l=y(n);r&&(t.className=r),l&&(t.style=l);const f={...e,...t,text:""};for(const u of Array.from(n.childNodes))E(u,f,s)}function I(i){const e=m(i),s=new p;s.use_classes=!0;const n=e.split(`
`);return typeof DOMParser>"u"?n.map(t=>[{text:t}]):n.map(t=>{const r=s.ansi_to_html(t),l=new DOMParser().parseFromString(`<body>${r}</body>`,"text/html"),f=[];for(const u of Array.from(l.body.childNodes))E(u,{text:""},f);return f})}function L(i){const e=m(i),s=new p;s.use_classes=!0;const n=s.ansi_to_html(e);if(typeof DOMParser>"u")return[e];const t=new DOMParser().parseFromString(`<body>${n}</body>`,"text/html");return Array.from(t.body.childNodes).map((r,l)=>S(r,String(l)))}export{I as a,L as b};
