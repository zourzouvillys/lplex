import { Bonjour } from "bonjour-service";

/**
 * Discover an lplex server via mDNS (_lplex._tcp).
 * Returns the server URL on success, throws on timeout.
 */
export function discover(timeoutMs = 3000): Promise<string> {
  return new Promise((resolve, reject) => {
    const instance = new Bonjour();
    const timer = setTimeout(() => {
      browser.stop();
      instance.destroy();
      reject(new Error("no _lplex._tcp service found on the network"));
    }, timeoutMs);

    const browser = instance.find({ type: "lplex" }, (service) => {
      clearTimeout(timer);
      browser.stop();
      instance.destroy();

      const port = service.port;

      // Prefer IPv4.
      const ipv4 = service.addresses?.find(
        (a) => a.includes(".") && !a.includes(":"),
      );
      if (ipv4) {
        resolve(`http://${ipv4}:${port}`);
        return;
      }

      // Fall back to IPv6.
      const ipv6 = service.addresses?.[0];
      if (ipv6) {
        resolve(`http://[${ipv6}]:${port}`);
        return;
      }

      // Last resort: use the hostname.
      resolve(`http://${service.host}:${port}`);
    });
  });
}
