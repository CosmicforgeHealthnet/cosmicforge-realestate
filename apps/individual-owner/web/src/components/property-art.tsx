/**
 * Original, locally-rendered architectural illustration for fictional mock properties.
 * No third-party photography, unstable URL, or implied verified property imagery.
 */
export function PropertyArt({variant=0}:{variant?:number}) {
  const kind=((variant%3)+3)%3;
  return <svg className={`property-art estate-art estate-art-${kind}`} viewBox="0 0 520 278" preserveAspectRatio="xMidYMid slice" aria-hidden="true">
    <defs>
      <linearGradient id="estate-sky" x1="0" y1="0" x2="1" y2="1">
        <stop offset="0%" stopColor="#B5CFEC"/>
        <stop offset="52%" stopColor="#E6EFFA"/>
        <stop offset="100%" stopColor="#E9E1D5"/>
      </linearGradient>
      <linearGradient id="estate-facade" x1="0" y1="0" x2="1" y2="1">
        <stop offset="0%" stopColor="#F2F0EC"/>
        <stop offset="100%" stopColor="#B3BCC6"/>
      </linearGradient>
      <linearGradient id="estate-glass" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stopColor="#6F9FB9"/>
        <stop offset="100%" stopColor="#234B6C"/>
      </linearGradient>
      <linearGradient id="estate-earth" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stopColor="#8C9B8A"/>
        <stop offset="100%" stopColor="#526E60"/>
      </linearGradient>
    </defs>
    <rect width="520" height="278" fill="url(#estate-sky)"/>
    <circle cx="438" cy="54" r="37" fill="#FFFFFF" opacity=".52"/>
    <path d="M0 169L85 151L170 167L245 144L335 157L415 130L520 148V278H0Z" fill="#A5B8B2" opacity=".40"/>
    {kind===0 ? <>
      <path d="M56 103h105v141H56z" fill="#B3BCC3" opacity=".6"/>
      <rect x="144" y="35" width="189" height="211" rx="3" fill="url(#estate-facade)"/>
      <path d="M335 68L424 107V246H335Z" fill="#8294A5"/>
      <path d="M167 51h145v170H167z" fill="url(#estate-glass)"/>
      {Array.from({length:6},(_,i)=><g key={i}>
        <path d={`M167 ${76+i*25}h145`} stroke="#ECF3FA" strokeWidth="7"/>
        <path d={`M335 ${120+i*21}h73`} stroke="#D5DEE8" strokeWidth="6"/>
      </g>)}
      {Array.from({length:5},(_,i)=><path key={i} d={`M${193+i*27} 51v170`} stroke="#C8DBE7" strokeWidth="4"/>)}
      <path d="M138 222h300v23H138z" fill="#E3E5E6"/>
      <path d="M167 247V35" stroke="#FFFFFF" strokeWidth="7"/>
      <path d="M144 36h191" stroke="#FAFAFC" strokeWidth="11"/>
    </> : kind===1 ? <>
      <path d="M93 112L260 50L433 110V234H93Z" fill="#D8D2C7"/>
      <path d="M78 113L260 35L448 112" fill="none" stroke="#4E6577" strokeWidth="18" strokeLinejoin="round"/>
      <path d="M113 125h294v114H113z" fill="url(#estate-facade)"/>
      <rect x="139" y="136" width="96" height="75" rx="2" fill="url(#estate-glass)"/>
      <rect x="278" y="136" width="101" height="75" rx="2" fill="url(#estate-glass)"/>
      <path d="M187 136v75M330 136v75" stroke="#DFEDF7" strokeWidth="5"/>
      <rect x="241" y="167" width="30" height="72" fill="#606F7B"/>
      <path d="M97 210h326v22H97z" fill="#FBF9F6"/>
      <path d="M0 240h520" stroke="#CAD1C9" strokeWidth="18"/>
    </> : <>
      <path d="M0 143c115-28 148-16 262 7 95 19 171-22 258-29v157H0Z" fill="#ADC0A5"/>
      <path d="M0 191c154-39 223 21 329-12 77-24 128-32 191-20v119H0Z" fill="url(#estate-earth)"/>
      <path d="M20 264L185 182L430 200L492 278" fill="none" stroke="#EAD8AF" strokeWidth="10" opacity=".95"/>
      <path d="M183 184L287 142L432 198" stroke="#DFE9D7" strokeWidth="4" strokeDasharray="9 9" fill="none"/>
      <path d="M286 144l-5-50" stroke="#506E61" strokeWidth="6"/>
      <path d="M279 119c-37-32-42-44-9-67 28 3 43 21 54 47-7 24-19 31-45 20z" fill="#577768"/>
      <path d="M96 176l-3-41M441 176l3-38" stroke="#52695B" strokeWidth="5"/>
      <circle cx="91" cy="127" r="25" fill="#557966"/>
      <circle cx="444" cy="125" r="31" fill="#628774"/>
      <path d="M363 203v-30h50v30" stroke="#F6F0D6" strokeWidth="5" fill="none"/>
    </>}
    <path d="M0 245Q130 233 260 242T520 238V278H0Z" fill="#294E44" opacity=".18"/>
    <path d="M0 269Q118 244 233 261T520 250V278H0Z" fill="#274F47" opacity=".14"/>
    <g opacity=".78">
      <path d="M28 236v-39M488 233v-42" stroke="#4D675E" strokeWidth="5"/>
      <circle cx="28" cy="194" r="24" fill="#567B69"/>
      <circle cx="489" cy="190" r="27" fill="#547964"/>
      <circle cx="12" cy="214" r="17" fill="#7D9F78"/>
      <circle cx="508" cy="215" r="19" fill="#739B83"/>
    </g>
    <rect x="0" y="0" width="520" height="278" fill="#112844" opacity=".035"/>
  </svg>;
}
