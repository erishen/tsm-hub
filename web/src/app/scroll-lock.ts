/**
 * 弹窗滚动锁：弹窗打开时锁定 body 滚动，防止滚动穿透到母页面。
 * 组件在弹窗信号上挂 effect（见各组件），并在 ngOnDestroy 里兜底恢复。
 */
export function lockBody(locked: boolean): void {
  document.body.style.overflow = locked ? 'hidden' : '';
}

export function unlockBody(): void {
  document.body.style.overflow = '';
}
