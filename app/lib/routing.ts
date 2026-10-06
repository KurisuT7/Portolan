export type Route =
  | { page: "overview" }
  | { page: "servers" }
  | { page: "server"; id: string }
  | { page: "nodes"; server?: string }
  | { page: "forwards"; server?: string }
  | { page: "forward"; id: string };

// Accepts the current "#/forwards/<id>" form and the older "#forwards?server=<id>" links.
export function parseRoute(hash: string): Route {
  const [path, query = ""] = hash.replace(/^#\/?/, "").split("?");
  let section = "";
  let id = "";
  try {
    [section = "", id = ""] = path.split("/").map(decodeURIComponent);
  } catch {
    return { page: "overview" };
  }
  const server = new URLSearchParams(query).get("server") || undefined;
  if (section === "servers") return id ? { page: "server", id } : { page: "servers" };
  if (section === "forwards" && id) return { page: "forward", id };
  if (section === "nodes") return { page: "nodes", server };
  if (section === "forwards") return { page: "forwards", server };
  return { page: "overview" };
}

export function routeHref(route: Route) {
  switch (route.page) {
    case "servers":
      return "#/servers";
    case "server":
      return `#/servers/${encodeURIComponent(route.id)}`;
    case "forward":
      return `#/forwards/${encodeURIComponent(route.id)}`;
    case "nodes":
    case "forwards":
      return `#/${route.page}${route.server ? `?${new URLSearchParams({ server: route.server })}` : ""}`;
    default:
      return "#/";
  }
}
