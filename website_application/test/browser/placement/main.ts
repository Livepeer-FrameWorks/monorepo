import { mount } from "svelte";
import "./fixture.css";
import Showcase from "./Showcase.svelte";

mount(Showcase, { target: document.getElementById("app")! });
