import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import "./themeStore.svelte.js";

mount(App, { target: document.getElementById("app") });
