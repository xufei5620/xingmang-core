/** jsdom 缺 Pointer Capture / scrollIntoView，Radix 的 Dialog 与 Select 打开时会调用。
 *  与 ui-primitives/src/test-setup.ts 同源——页面级测试要开对话框，同样需要它们。 */
if (typeof HTMLElement !== "undefined") {
  const proto = HTMLElement.prototype;
  if (!proto.hasPointerCapture) proto.hasPointerCapture = () => false;
  if (!proto.setPointerCapture) proto.setPointerCapture = () => {};
  if (!proto.releasePointerCapture) proto.releasePointerCapture = () => {};
  if (!proto.scrollIntoView) proto.scrollIntoView = () => {};
}
