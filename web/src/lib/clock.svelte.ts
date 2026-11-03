// tick is the time relative times are counted from: it moves every 15 s, so every <Rel> stays current.
export const tick = $state({ now: Date.now() / 1000 });
setInterval(() => { tick.now = Date.now() / 1000; }, 15000);
