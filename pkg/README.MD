# Shared MicroService Packages

This directory provides middleware and internal libraries for shared functionality between Microservices (auth, db, types, logging, ...)

Any import that requires boilerplate code and gets used by multiple microservices: wrap it in an internal pkg and make sure to expose the required types and handlers, so that the microservice doesn't even have to import the underlying package directly.
