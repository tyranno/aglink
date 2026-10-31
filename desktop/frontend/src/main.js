import { mount } from "svelte";
import "./app.css";

// "/?indicator" is the small voice-conversation window shown while the app is
// in the tray, "/?reply" the reply popup above it; neither may load the chat
// app (stores, connection) itself.
const target = document.getElementById("app");
const params = new URLSearchParams(location.search);
if (params.has("indicator")) {
  import("./Indicator.svelte").then(({ default: Indicator }) => mount(Indicator, { target }));
} else if (params.has("reply")) {
  import("./ReplyToast.svelte").then(({ default: ReplyToast }) => mount(ReplyToast, { target }));
} else {
  import("./themeStore.svelte.js");
  import("./App.svelte").then(({ default: App }) => mount(App, { target }));
}
