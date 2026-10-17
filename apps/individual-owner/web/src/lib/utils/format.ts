export function money(minor: number) {
  return new Intl.NumberFormat('en-NG', {style:'currency',currency:'NGN',maximumFractionDigits:0}).format(minor / 100);
}
