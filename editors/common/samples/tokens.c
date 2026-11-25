/*
 * The tokens of DGD LPC, one class at a time: a sample for the grammar
 * tests under editors/, not an object anybody should compile.
 */

inherit "/lib/base";
inherit room "/std/room";
private inherit util UTIL_PATH;

// A line comment (dialect.slash_slash) ends with its line, whatever \
static int after_the_comment;

/* A block comment keeps "this" and // this and 'x' and #if to itself,
   over as many lines as it takes. */

private static mapping exits;
static nomask atomic varargs mixed *table;
object owner; float weight; string name; int count;

static mixed
literals(string str, int *arr)
{
    float f;
    int i;

    f = 1.0 + 1. + .5 + 1e10 + 1.5e-3 + 2E+4 + 0.25;
    i = 0 + 7 + 42 + 0x1f + 0XFF + 017;
    i = 'a' + '\n' + '\'' + '\\' + '\101' + '\x41' + '"' + '/';
    str = "plain" + "esc \n \t \" \\ \101 \x41 \q" + "" + "/* no comment */";
    str = "continued \
on the next line" + "// no comment";
    arr = arr[1..2] + arr[..3] + arr[i..] + arr[0 .. 1] + arr[i + 1 ..];
    return ({ str, f, i, nil, ([ "a": 1, "b": ({ 2, 3 }), arr[0]: arr[1] ]) });
}

static int
operators(int a, int b, object ob, mixed args...)
{
    a = b; a += b; a -= b; a *= b; a /= b; a %= b;
    a &= b; a |= b; a ^= b; a <<= b; a >>= b;
    a = a + b - a * b / a % b;
    a = a & b | a ^ ~b;
    a = a << 2 >> 1;
    a = a == b || a != b && a < b || a > b && a <= b || a >= b || !a;
    a++; b--; ++a; --b;
    a = a ? b : -a;
    a = ob <- "/std/room";
    ob->call(a, b, args...);
    ob = new object "/data/thing";
    room::create();
    ::create();
    return f(a[i]) + g(([ ]), ({ }), ([ a: b[0] ])[a]) + sizeof(({ a, b }));
}

static void
keywords(int n, string function)
{
    int i;

    if (n)
        n = 1;
    else if (!n) {
        n = 2;
    } else
        return;
    for (i = 0; i < n; i++)
        continue;
    while (n--)
        break;
    do {
        n++;
    } while (n < 10);
    switch (n) {
    case 1:
    case 2..3:
        break;
    case 'a':
        goto out;
    default:
        return;
    }
    catch {
        n = 1 / 0;
    } : {
        n = 0;
    }
    try {
        n = 1;
    } catch (err) {
        n = 2;
    }
    rlimits (10; 1000) {
        function = catch(call_other(this_object(), function, n));
    }
out:
    NAME = MAX_COUNT + __DGD__ + _x + X + T1;
}

static int
operator+ (int n)
{
    return count + n;
}
