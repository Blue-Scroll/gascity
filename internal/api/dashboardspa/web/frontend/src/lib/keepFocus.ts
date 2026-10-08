// Tapping a button moves focus to it, and on a phone that closes the software
// keyboard — which is wrong for every control that sits around a text box the
// operator is still typing in. Preventing the default on mousedown stops the
// focus transfer while leaving the click itself alone; iOS synthesises that
// mousedown from the tap, so this covers touch as well as a trackpad.
export function keepFocus(e: { preventDefault: () => void }): void {
  e.preventDefault();
}
